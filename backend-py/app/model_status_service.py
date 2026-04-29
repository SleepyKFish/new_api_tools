"""
Model Status Monitoring Service for NewAPI Middleware Tool.
Provides sliding window status monitoring based on log data.
"""
import time
from dataclasses import dataclass, asdict, field
from typing import Any, Dict, List, Optional, Tuple
from datetime import datetime, timedelta

from .database import DatabaseEngine, get_db_manager
from .local_storage import get_local_storage
from .logger import logger

# Constants
LOG_TYPE_CONSUMPTION = 2  # type=2 is consumption/usage log (success)
LOG_TYPE_FAILURE = 5  # type=5 is failure log (request failed)

# Cache TTL in seconds
# Short TTL for actively monitored models (selected models refresh frequently)
CACHE_TTL_SHORT = 30  # 30 seconds for selected models
# Long TTL for warmup/background cache (unselected models, reduce DB load)
CACHE_TTL_LONG = 300  # 5 minutes for warmup cache

# Time window configurations: (total_seconds, num_slots, slot_seconds)
TIME_WINDOWS = {
    "15m": (900, 15, 60),        # 15 minutes, 15 slots, 1 minute each
    "30m": (1800, 30, 60),       # 30 minutes, 30 slots, 1 minute each
    "1h": (3600, 60, 60),        # 1 hour, 60 slots, 1 minute each
    "6h": (21600, 24, 900),      # 6 hours, 24 slots, 15 minutes each
    "12h": (43200, 24, 1800),    # 12 hours, 24 slots, 30 minutes each
    "24h": (86400, 24, 3600),    # 24 hours, 24 slots, 1 hour each
}

DEFAULT_TIME_WINDOW = "24h"


@dataclass
class SlotStatus:
    """Status data for a time slot."""
    slot: int  # slot index (0 = oldest, N-1 = newest)
    start_time: int  # Unix timestamp
    end_time: int  # Unix timestamp
    total_requests: int
    success_count: int
    success_rate: float  # 0-100
    status: str  # 'green', 'yellow', 'red'


@dataclass
class ModelStatus:
    """Model status with time window history."""
    model_name: str
    display_name: str
    time_window: str  # '1h', '6h', '12h', '24h'
    total_requests: int
    success_count: int
    success_rate: float
    current_status: str  # 'green', 'yellow', 'red'
    within_5s_rate: Optional[float] = None
    within_10s_rate: Optional[float] = None
    duration_within_10s_rate: Optional[float] = None
    duration_within_20s_rate: Optional[float] = None
    cache_hit_rate: Optional[float] = None
    completion_tps: Optional[float] = None
    timed_requests: int = 0
    duration_timed_requests: int = 0
    output_requests: int = 0
    slot_data: List[SlotStatus] = field(default_factory=list)


@dataclass
class ChannelPerformanceSummary:
    """Performance summary for a channel within a time window."""
    channel_id: int
    channel_name: str
    total_requests: int
    within_5s_rate: Optional[float] = None
    within_10s_rate: Optional[float] = None
    duration_within_10s_rate: Optional[float] = None
    duration_within_20s_rate: Optional[float] = None
    cache_hit_rate: Optional[float] = None
    completion_tps: Optional[float] = None
    timed_requests: int = 0
    duration_timed_requests: int = 0
    output_requests: int = 0


def get_status_color(success_rate: float, total_requests: int) -> str:
    """
    Determine status color based on success rate.
    
    Args:
        success_rate: Success rate percentage (0-100)
        total_requests: Total number of requests
        
    Returns:
        'green', 'yellow', or 'red'
    """
    if total_requests == 0:
        return 'green'  # No requests = no issues
    if success_rate >= 95:
        return 'green'
    elif success_rate >= 80:
        return 'yellow'
    else:
        return 'red'


def get_time_window_config(window: str) -> Tuple[int, int, int]:
    """Get time window configuration."""
    return TIME_WINDOWS.get(window, TIME_WINDOWS[DEFAULT_TIME_WINDOW])


class ModelStatusService:
    """
    Service for model status monitoring.
    Provides sliding window status based on log data.
    """

    def __init__(self):
        self._db = get_db_manager()
        self._storage = get_local_storage()
        self._init_cache_table()

    def _init_cache_table(self):
        """Initialize cache table in SQLite."""
        with self._storage._get_connection() as conn:
            cursor = conn.cursor()
            cursor.execute("""
                CREATE TABLE IF NOT EXISTS model_status_cache (
                    cache_key TEXT PRIMARY KEY,
                    data TEXT NOT NULL,
                    created_at INTEGER NOT NULL,
                    expires_at INTEGER NOT NULL
                )
            """)
            cursor.execute("""
                CREATE INDEX IF NOT EXISTS idx_model_status_expires 
                ON model_status_cache(expires_at)
            """)
            conn.commit()

    def _get_cache(self, key: str) -> Optional[Dict]:
        """Get cached data if not expired."""
        import json
        now = int(time.time())
        with self._storage._get_connection() as conn:
            cursor = conn.cursor()
            cursor.execute(
                "SELECT data FROM model_status_cache WHERE cache_key = ? AND expires_at > ?",
                (key, now)
            )
            row = cursor.fetchone()
            if row:
                return json.loads(row[0])
        return None

    def _set_cache(self, key: str, data: Dict, ttl: int = CACHE_TTL_SHORT):
        """Set cache with TTL."""
        import json
        now = int(time.time())
        expires_at = now + ttl
        with self._storage._get_connection() as conn:
            cursor = conn.cursor()
            cursor.execute("""
                INSERT OR REPLACE INTO model_status_cache (cache_key, data, created_at, expires_at)
                VALUES (?, ?, ?, ?)
            """, (key, json.dumps(data), now, expires_at))
            conn.commit()

    def _clear_expired_cache(self):
        """Clear expired cache entries."""
        now = int(time.time())
        with self._storage._get_connection() as conn:
            cursor = conn.cursor()
            cursor.execute("DELETE FROM model_status_cache WHERE expires_at < ?", (now,))
            conn.commit()

    def _build_performance_summary(
        self,
        total_requests: int,
        timed_requests: int,
        output_requests: int,
        within_5s: int,
        within_10s: int,
        duration_timed_requests: int,
        duration_within_10s: int,
        duration_within_20s: int,
        total_cache_denominator_tokens: float,
        total_cache_tokens: float,
        total_completion_tokens: float,
        total_use_time: float,
    ) -> Dict[str, Any]:
        """Convert raw aggregates into frontend-friendly performance metrics."""
        within_5s_rate = round(within_5s / timed_requests * 100, 2) if timed_requests > 0 else None
        within_10s_rate = round(within_10s / timed_requests * 100, 2) if timed_requests > 0 else None
        duration_within_10s_rate = round(duration_within_10s / duration_timed_requests * 100, 2) if duration_timed_requests > 0 else None
        duration_within_20s_rate = round(duration_within_20s / duration_timed_requests * 100, 2) if duration_timed_requests > 0 else None
        cache_hit_rate = round(min(total_cache_tokens, total_cache_denominator_tokens) / total_cache_denominator_tokens * 100, 2) if total_cache_denominator_tokens > 0 else None
        completion_tps = round(total_completion_tokens / total_use_time, 2) if total_use_time > 0 else None
        return {
            "total_requests": total_requests,
            "within_5s_rate": within_5s_rate,
            "within_10s_rate": within_10s_rate,
            "duration_within_10s_rate": duration_within_10s_rate,
            "duration_within_20s_rate": duration_within_20s_rate,
            "cache_hit_rate": cache_hit_rate,
            "completion_tps": completion_tps,
            "timed_requests": timed_requests,
            "duration_timed_requests": duration_timed_requests,
            "output_requests": output_requests,
        }

    def _column_exists(self, table_name: str, column_name: str) -> bool:
        """Check whether a column exists in the upstream NewAPI database."""
        try:
            self._db.connect()
            if self._db.config.engine == DatabaseEngine.POSTGRESQL:
                sql = """
                    SELECT 1
                    FROM information_schema.columns
                    WHERE table_name = :table_name
                      AND column_name = :column_name
                    LIMIT 1
                """
            else:
                sql = """
                    SELECT 1
                    FROM information_schema.columns
                    WHERE table_schema = DATABASE()
                      AND table_name = :table_name
                      AND column_name = :column_name
                    LIMIT 1
                """
            return bool(self._db.execute(sql, {
                "table_name": table_name,
                "column_name": column_name,
            }))
        except Exception:
            return False

    def _first_token_time_expr(self, alias: str = "") -> str:
        """
        Build a SQL expression for TTFT seconds.

        NewAPI records stream first response time in logs.other.frt as milliseconds.
        Non-stream requests do not have an earlier token boundary, so their TTFT is
        the normal returned use_time.
        """
        prefix = f"{alias}." if alias else ""
        use_time = f"{prefix}use_time"
        if not (self._column_exists("logs", "is_stream") and self._column_exists("logs", "other")):
            return use_time

        other = f"{prefix}other"
        is_stream = f"{prefix}is_stream"
        if self._db.config.engine == DatabaseEngine.POSTGRESQL:
            frt_ms = f"CAST(CAST(NULLIF(NULLIF({other}, ''), 'null') AS jsonb)->>'frt' AS DOUBLE PRECISION)"
            stream_check = f"COALESCE({is_stream}, false) = true"
        else:
            frt_ms = f"CAST(JSON_UNQUOTE(JSON_EXTRACT({other}, '$.frt')) AS DECIMAL(20,6))"
            stream_check = f"COALESCE({is_stream}, 0) = 1"

        if self._db.config.engine == DatabaseEngine.POSTGRESQL:
            return f"""
                CASE
                    WHEN {stream_check}
                         AND {other} IS NOT NULL
                         AND {other} <> ''
                         AND {other} <> 'null'
                         AND COALESCE({frt_ms}, 0) > 0
                    THEN {frt_ms} / 1000.0
                    ELSE {use_time}
                END
            """

        return f"""
            CASE
                WHEN {stream_check}
                     AND {other} IS NOT NULL
                     AND {other} <> ''
                     AND JSON_VALID({other})
                     AND COALESCE({frt_ms}, 0) > 0
                THEN {frt_ms} / 1000.0
                ELSE {use_time}
            END
        """

    def _json_number_expr(self, json_expr: str, key: str) -> str:
        """Build a SQL expression that extracts a numeric value from a JSON object."""
        if self._db.config.engine == DatabaseEngine.POSTGRESQL:
            return f"""
                CASE
                    WHEN {json_expr} IS NOT NULL AND {json_expr} <> '' AND {json_expr} <> 'null'
                    THEN CAST(CAST({json_expr} AS jsonb)->>'{key}' AS DOUBLE PRECISION)
                    ELSE 0
                END
            """
        return f"""
            CASE
                WHEN {json_expr} IS NOT NULL AND {json_expr} <> '' AND JSON_VALID({json_expr})
                THEN CAST(JSON_UNQUOTE(JSON_EXTRACT({json_expr}, '$.{key}')) AS DECIMAL(20,6))
                ELSE 0
            END
        """

    def _json_text_expr(self, json_expr: str, key: str) -> str:
        """Build a SQL expression that extracts a text value from a JSON object."""
        if self._db.config.engine == DatabaseEngine.POSTGRESQL:
            return f"""
                CASE
                    WHEN {json_expr} IS NOT NULL AND {json_expr} <> '' AND {json_expr} <> 'null'
                    THEN CAST({json_expr} AS jsonb)->>'{key}'
                    ELSE NULL
                END
            """
        return f"""
            CASE
                WHEN {json_expr} IS NOT NULL AND {json_expr} <> '' AND JSON_VALID({json_expr})
                THEN JSON_UNQUOTE(JSON_EXTRACT({json_expr}, '$.{key}'))
                ELSE NULL
            END
        """

    def _request_path_expr(self, other_col: str) -> str:
        """Best-effort request path extraction from NewAPI logs.other."""
        return "LOWER(COALESCE(" + ", ".join([
            self._json_text_expr(other_col, "请求路径"),
            self._json_text_expr(other_col, "request_path"),
            self._json_text_expr(other_col, "path"),
            self._json_text_expr(other_col, "endpoint"),
            self._json_text_expr(other_col, "url"),
            self._json_text_expr(other_col, "request_url"),
            "''",
        ]) + "))"

    def _request_conversion_expr(self, other_col: str) -> str:
        """Best-effort request conversion extraction from NewAPI logs.other."""
        return "LOWER(COALESCE(" + ", ".join([
            self._json_text_expr(other_col, "request_conversion"),
            self._json_text_expr(other_col, "conversion"),
            self._json_text_expr(other_col, "request_format"),
            self._json_text_expr(other_col, "request_relay_format"),
            self._json_text_expr(other_col, "final_request_relay_format"),
            self._json_text_expr(other_col, "relay_format"),
            "''",
        ]) + "))"

    def _json_bool_expr(self, json_expr: str, key: str) -> str:
        """Build a SQL expression that extracts a boolean-ish JSON value."""
        return f"LOWER(COALESCE({self._json_text_expr(json_expr, key)}, '')) IN ('true', '1')"

    def _cache_tokens_sum_select(self, alias: str = "") -> str:
        """Build a cache token aggregate that is safe for older NewAPI schemas."""
        if not self._column_exists("logs", "other"):
            return "0 as cache_tokens_sum"

        prefix = f"{alias}." if alias else ""
        type_col = f"{prefix}type"
        other_col = f"{prefix}other"
        cache_tokens = self._json_number_expr(other_col, "cache_tokens")
        return f"""
            COALESCE(SUM(CASE
                WHEN {type_col} = :type_success
                     AND {other_col} IS NOT NULL
                     AND {other_col} <> ''
                     AND ({cache_tokens}) > 0
                THEN ({cache_tokens})
                ELSE 0
            END), 0) as cache_tokens_sum
        """

    def _cache_denominator_sum_select(self, alias: str = "") -> str:
        """Build cache hit denominator, with Anthropic messages format adjustment."""
        prefix = f"{alias}." if alias else ""
        type_col = f"{prefix}type"
        prompt_col = f"{prefix}prompt_tokens"
        if not self._column_exists("logs", "other"):
            return f"COALESCE(SUM(CASE WHEN {type_col} = :type_success AND {prompt_col} > 0 THEN {prompt_col} ELSE 0 END), 0) as cache_denominator_sum"

        other_col = f"{prefix}other"
        cache_tokens = self._json_number_expr(other_col, "cache_tokens")
        request_path = self._request_path_expr(other_col)
        request_conversion = self._request_conversion_expr(other_col)
        is_claude = self._json_bool_expr(other_col, "claude")
        return f"""
            COALESCE(SUM(CASE
                WHEN {type_col} = :type_success
                THEN CASE
                    WHEN {is_claude}
                         OR {request_conversion} LIKE '%-> claude messages%'
                         OR {request_conversion} LIKE '%->claude messages%'
                         OR {request_conversion} = 'claude messages'
                         OR ({request_conversion} = '' AND {request_path} LIKE '%/v1/messages%')
                    THEN COALESCE({prompt_col}, 0) + COALESCE(({cache_tokens}), 0)
                    ELSE GREATEST(COALESCE({prompt_col}, 0), COALESCE(({cache_tokens}), 0))
                END
                ELSE 0
            END), 0) as cache_denominator_sum
        """

    def _get_model_performance_map(
        self,
        model_names: List[str],
        window_start: int,
        now: int,
    ) -> Dict[str, Dict[str, Any]]:
        """Get summary performance metrics for a batch of models."""
        if not model_names:
            return {}

        model_placeholders = ", ".join([f":perf_model_{i}" for i in range(len(model_names))])
        first_token_time = self._first_token_time_expr()
        cache_tokens_sum_select = self._cache_tokens_sum_select()
        cache_denominator_sum_select = self._cache_denominator_sum_select()
        sql = f"""
            SELECT
                model_name,
                SUM(CASE WHEN type = :type_success THEN 1 ELSE 0 END) as success_requests,
                SUM(CASE WHEN type = :type_success AND ({first_token_time}) > 0 THEN 1 ELSE 0 END) as timed_requests,
                SUM(CASE WHEN type = :type_success AND ({first_token_time}) > 0 AND ({first_token_time}) <= 5 THEN 1 ELSE 0 END) as within_5s,
                SUM(CASE WHEN type = :type_success AND ({first_token_time}) > 0 AND ({first_token_time}) <= 10 THEN 1 ELSE 0 END) as within_10s,
                SUM(CASE WHEN type = :type_success AND use_time > 0 THEN 1 ELSE 0 END) as duration_timed_requests,
                SUM(CASE WHEN type = :type_success AND use_time > 0 AND use_time <= 10 THEN 1 ELSE 0 END) as duration_within_10s,
                SUM(CASE WHEN type = :type_success AND use_time > 0 AND use_time <= 20 THEN 1 ELSE 0 END) as duration_within_20s,
                SUM(CASE WHEN type = :type_success AND completion_tokens > 0 AND use_time > 0 THEN 1 ELSE 0 END) as output_requests,
                {cache_denominator_sum_select},
                {cache_tokens_sum_select},
                COALESCE(SUM(CASE WHEN type = :type_success AND completion_tokens > 0 AND use_time > 0 THEN completion_tokens ELSE 0 END), 0) as completion_tokens_sum,
                COALESCE(SUM(CASE WHEN type = :type_success AND completion_tokens > 0 AND use_time > 0 THEN use_time ELSE 0 END), 0) as use_time_sum
            FROM logs
            WHERE model_name IN ({model_placeholders})
              AND created_at >= :window_start
              AND created_at < :now
              AND type IN (:type_success, :type_failure)
            GROUP BY model_name
        """
        params = {
            "window_start": window_start,
            "now": now,
            "type_success": LOG_TYPE_CONSUMPTION,
            "type_failure": LOG_TYPE_FAILURE,
        }
        for i, model_name in enumerate(model_names):
            params[f"perf_model_{i}"] = model_name

        performance_map = {}
        try:
            self._db.connect()
            result = self._db.execute(sql, params)
            for row in result:
                model_name = row.get("model_name")
                if not model_name:
                    continue
                performance_map[model_name] = self._build_performance_summary(
                    total_requests=int(row.get("success_requests") or 0),
                    timed_requests=int(row.get("timed_requests") or 0),
                    output_requests=int(row.get("output_requests") or 0),
                    within_5s=int(row.get("within_5s") or 0),
                    within_10s=int(row.get("within_10s") or 0),
                    duration_timed_requests=int(row.get("duration_timed_requests") or 0),
                    duration_within_10s=int(row.get("duration_within_10s") or 0),
                    duration_within_20s=int(row.get("duration_within_20s") or 0),
                    total_cache_denominator_tokens=float(row.get("cache_denominator_sum") or 0),
                    total_cache_tokens=float(row.get("cache_tokens_sum") or 0),
                    total_completion_tokens=float(row.get("completion_tokens_sum") or 0),
                    total_use_time=float(row.get("use_time_sum") or 0),
                )
        except Exception as e:
            logger.db_error(f"批量获取模型性能摘要失败: {e}")

        return performance_map

    def get_channel_performance_summaries(
        self,
        time_window: str = DEFAULT_TIME_WINDOW,
        use_cache: bool = True,
    ) -> List[ChannelPerformanceSummary]:
        """Get performance summaries grouped by channel."""
        if time_window not in TIME_WINDOWS:
            time_window = DEFAULT_TIME_WINDOW

        cache_key = f"channel_performance:{time_window}"
        if use_cache:
            cached = self._get_cache(cache_key)
            if cached:
                return [ChannelPerformanceSummary(**item) for item in cached.get("channels", [])]

        now = int(time.time())
        total_seconds, _, _ = get_time_window_config(time_window)
        window_start = now - total_seconds
        first_token_time = self._first_token_time_expr("l")
        cache_tokens_sum_select = self._cache_tokens_sum_select("l")
        cache_denominator_sum_select = self._cache_denominator_sum_select("l")

        sql = f"""
            SELECT
                c.id as channel_id,
                COALESCE(c.name, '') as channel_name,
                SUM(CASE WHEN l.type = :type_success THEN 1 ELSE 0 END) as success_requests,
                SUM(CASE WHEN l.type = :type_success AND ({first_token_time}) > 0 THEN 1 ELSE 0 END) as timed_requests,
                SUM(CASE WHEN l.type = :type_success AND ({first_token_time}) > 0 AND ({first_token_time}) <= 5 THEN 1 ELSE 0 END) as within_5s,
                SUM(CASE WHEN l.type = :type_success AND ({first_token_time}) > 0 AND ({first_token_time}) <= 10 THEN 1 ELSE 0 END) as within_10s,
                SUM(CASE WHEN l.type = :type_success AND l.use_time > 0 THEN 1 ELSE 0 END) as duration_timed_requests,
                SUM(CASE WHEN l.type = :type_success AND l.use_time > 0 AND l.use_time <= 10 THEN 1 ELSE 0 END) as duration_within_10s,
                SUM(CASE WHEN l.type = :type_success AND l.use_time > 0 AND l.use_time <= 20 THEN 1 ELSE 0 END) as duration_within_20s,
                SUM(CASE WHEN l.type = :type_success AND l.completion_tokens > 0 AND l.use_time > 0 THEN 1 ELSE 0 END) as output_requests,
                {cache_denominator_sum_select},
                {cache_tokens_sum_select},
                COALESCE(SUM(CASE WHEN l.type = :type_success AND l.completion_tokens > 0 AND l.use_time > 0 THEN l.completion_tokens ELSE 0 END), 0) as completion_tokens_sum,
                COALESCE(SUM(CASE WHEN l.type = :type_success AND l.completion_tokens > 0 AND l.use_time > 0 THEN l.use_time ELSE 0 END), 0) as use_time_sum
            FROM logs l
            INNER JOIN channels c ON c.id = l.channel_id
            WHERE l.created_at >= :window_start
              AND l.created_at < :now
              AND l.type IN (:type_success, :type_failure)
            GROUP BY c.id, c.name
            HAVING SUM(CASE WHEN l.type = :type_success THEN 1 ELSE 0 END) > 0
            ORDER BY success_requests DESC, channel_id ASC
        """

        channels: List[ChannelPerformanceSummary] = []
        try:
            self._db.connect()
            result = self._db.execute(sql, {
                "window_start": window_start,
                "now": now,
                "type_success": LOG_TYPE_CONSUMPTION,
                "type_failure": LOG_TYPE_FAILURE,
            })
            for row in result:
                perf = self._build_performance_summary(
                    total_requests=int(row.get("success_requests") or 0),
                    timed_requests=int(row.get("timed_requests") or 0),
                    output_requests=int(row.get("output_requests") or 0),
                    within_5s=int(row.get("within_5s") or 0),
                    within_10s=int(row.get("within_10s") or 0),
                    duration_timed_requests=int(row.get("duration_timed_requests") or 0),
                    duration_within_10s=int(row.get("duration_within_10s") or 0),
                    duration_within_20s=int(row.get("duration_within_20s") or 0),
                    total_cache_denominator_tokens=float(row.get("cache_denominator_sum") or 0),
                    total_cache_tokens=float(row.get("cache_tokens_sum") or 0),
                    total_completion_tokens=float(row.get("completion_tokens_sum") or 0),
                    total_use_time=float(row.get("use_time_sum") or 0),
                )
                channels.append(ChannelPerformanceSummary(
                    channel_id=int(row.get("channel_id") or 0),
                    channel_name=row.get("channel_name") or f"Channel#{row.get('channel_id')}",
                    total_requests=perf["total_requests"],
                    within_5s_rate=perf["within_5s_rate"],
                    within_10s_rate=perf["within_10s_rate"],
                    duration_within_10s_rate=perf["duration_within_10s_rate"],
                    duration_within_20s_rate=perf["duration_within_20s_rate"],
                    cache_hit_rate=perf["cache_hit_rate"],
                    completion_tps=perf["completion_tps"],
                    timed_requests=perf["timed_requests"],
                    duration_timed_requests=perf["duration_timed_requests"],
                    output_requests=perf["output_requests"],
                ))
        except Exception as e:
            logger.db_error(f"获取渠道性能摘要失败: {e}")
            return []

        self._set_cache(
            cache_key,
            {"channels": [asdict(channel) for channel in channels]},
            ttl=CACHE_TTL_SHORT,
        )
        return channels

    def get_available_models(self, use_cache: bool = True) -> List[str]:
        """
        Get list of all models from online channels (abilities table).
        Returns models that are enabled in active channels (status=1).

        Args:
            use_cache: Whether to use cached data (default: True).

        Returns:
            List of model names from online channels.
        """
        cache_key = "available_models"
        if use_cache:
            cached = self._get_cache(cache_key)
            if cached:
                return cached.get("models", [])

        # Query models from abilities table (online channels)
        # Join with channels to filter only active channels (status=1)
        from .database import DatabaseEngine
        is_pg = self._db.config.engine == DatabaseEngine.POSTGRESQL

        sql = """
            SELECT DISTINCT a.model as model_name
            FROM abilities a
            INNER JOIN channels c ON c.id = a.channel_id
            WHERE c.status = 1
        """
        # Filter enabled abilities
        if is_pg:
            sql += " AND COALESCE(a.enabled, TRUE) = TRUE"
        else:
            sql += " AND COALESCE(a.enabled, 1) = 1"
        sql += " ORDER BY a.model"

        try:
            self._db.connect()
            result = self._db.execute(sql)
            models = [row["model_name"] for row in result if row.get("model_name")]
            self._set_cache(cache_key, {"models": models}, ttl=300)  # 5 min cache
            return models
        except Exception as e:
            logger.db_error(f"获取可用模型列表失败: {e}")
            return []

    def get_available_models_with_stats(self, use_cache: bool = True) -> List[Dict[str, Any]]:
        """
        Get list of all models with 24h request counts for sorting.
        Models are sorted by request count (descending), models with no requests at the end.

        Args:
            use_cache: Whether to use cached data (default: True).

        Returns:
            List of dicts with model_name and request_count_24h.
        """
        cache_key = "available_models_with_stats"
        if use_cache:
            cached = self._get_cache(cache_key)
            if cached:
                return cached.get("models", [])

        # First get all available models
        all_models = self.get_available_models(use_cache=use_cache)
        if not all_models:
            return []

        # Query 24h request counts for all models
        now = int(time.time())
        start_24h = now - 86400

        sql = """
            SELECT model_name, COUNT(*) as request_count
            FROM logs
            WHERE created_at >= :start_time
              AND created_at < :now
              AND type IN (:type_success, :type_failure)
            GROUP BY model_name
        """

        request_counts = {}
        try:
            self._db.connect()
            result = self._db.execute(sql, {
                "start_time": start_24h,
                "now": now,
                "type_success": LOG_TYPE_CONSUMPTION,
                "type_failure": LOG_TYPE_FAILURE,
            })
            for row in result:
                model_name = row.get("model_name")
                if model_name:
                    request_counts[model_name] = int(row.get("request_count") or 0)
        except Exception as e:
            logger.db_error(f"获取模型请求统计失败: {e}")

        # Build result list with request counts
        models_with_stats = []
        for model in all_models:
            models_with_stats.append({
                "model_name": model,
                "request_count_24h": request_counts.get(model, 0),
            })

        # Sort: models with requests first (by count desc), then models without requests (alphabetically)
        models_with_stats.sort(key=lambda x: (-x["request_count_24h"], x["model_name"]))

        # 30 min cache - longer TTL to avoid slow queries when users access the page
        # This data doesn't change frequently and will be refreshed by background task
        self._set_cache(cache_key, {"models": models_with_stats}, ttl=1800)
        return models_with_stats

    def get_model_status(
        self,
        model_name: str,
        time_window: str = DEFAULT_TIME_WINDOW,
        use_cache: bool = True,
        cache_ttl: int = CACHE_TTL_SHORT
    ) -> Optional[ModelStatus]:
        """
        Get status for a specific model within a time window.

        Args:
            model_name: Name of the model to query.
            time_window: Time window ('15m', '30m', '1h', '6h', '12h', '24h').
            use_cache: Whether to use cached data.
            cache_ttl: Cache TTL in seconds (default: short TTL for active monitoring).

        Returns:
            ModelStatus with slot breakdown.
        """
        # Validate time window
        if time_window not in TIME_WINDOWS:
            time_window = DEFAULT_TIME_WINDOW

        cache_key = f"model_status:{model_name}:{time_window}"
        if use_cache:
            cached = self._get_cache(cache_key)
            if cached:
                return self._dict_to_model_status(cached)

        now = int(time.time())
        total_seconds, num_slots, slot_seconds = get_time_window_config(time_window)

        # Calculate time range
        window_start = now - total_seconds

        # Single optimized query - aggregate by time slot using floor division
        # This reduces N queries to 1 query per model
        sql = """
            SELECT
                FLOOR((created_at - :window_start) / :slot_seconds) as slot_idx,
                COUNT(*) as total,
                SUM(CASE WHEN type = :type_success THEN 1 ELSE 0 END) as success
            FROM logs
            WHERE model_name = :model_name
              AND created_at >= :window_start
              AND created_at < :now
              AND type IN (:type_success, :type_failure)
            GROUP BY FLOOR((created_at - :window_start) / :slot_seconds)
        """

        # Initialize all slots with zeros
        slot_data_map = {}
        for i in range(num_slots):
            slot_start = window_start + (i * slot_seconds)
            slot_end = slot_start + slot_seconds
            slot_data_map[i] = {
                'slot': i,
                'start_time': slot_start,
                'end_time': slot_end,
                'total_requests': 0,
                'success_count': 0,
            }

        try:
            self._db.connect()
            result = self._db.execute(sql, {
                "model_name": model_name,
                "window_start": window_start,
                "now": now,
                "slot_seconds": slot_seconds,
                "type_success": LOG_TYPE_CONSUMPTION,
                "type_failure": LOG_TYPE_FAILURE,
            })

            # Fill in actual data from query results
            for row in result:
                slot_idx = int(row.get("slot_idx") or 0)
                if 0 <= slot_idx < num_slots:
                    slot_data_map[slot_idx]['total_requests'] = int(row.get("total") or 0)
                    slot_data_map[slot_idx]['success_count'] = int(row.get("success") or 0)

        except Exception as e:
            logger.db_error(f"获取模型 {model_name} 状态失败: {e}")

        # Build slot_data list with status colors
        slot_data = []
        total_requests = 0
        total_success = 0
        performance = self._get_model_performance_map([model_name], window_start, now).get(model_name, {})

        for i in range(num_slots):
            slot_info = slot_data_map[i]
            slot_total = slot_info['total_requests']
            slot_success = slot_info['success_count']
            success_rate = (slot_success / slot_total * 100) if slot_total > 0 else 100.0
            status = get_status_color(success_rate, slot_total)

            slot_data.append(SlotStatus(
                slot=i,
                start_time=slot_info['start_time'],
                end_time=slot_info['end_time'],
                total_requests=slot_total,
                success_count=slot_success,
                success_rate=round(success_rate, 2),
                status=status,
            ))

            total_requests += slot_total
            total_success += slot_success

        overall_rate = (total_success / total_requests * 100) if total_requests > 0 else 100.0
        current_status = get_status_color(overall_rate, total_requests)

        model_status = ModelStatus(
            model_name=model_name,
            display_name=self._get_display_name(model_name),
            time_window=time_window,
            total_requests=total_requests,
            success_count=total_success,
            success_rate=round(overall_rate, 2),
            current_status=current_status,
            within_5s_rate=performance.get("within_5s_rate"),
            within_10s_rate=performance.get("within_10s_rate"),
            duration_within_10s_rate=performance.get("duration_within_10s_rate"),
            duration_within_20s_rate=performance.get("duration_within_20s_rate"),
            cache_hit_rate=performance.get("cache_hit_rate"),
            completion_tps=performance.get("completion_tps"),
            timed_requests=performance.get("timed_requests", 0),
            duration_timed_requests=performance.get("duration_timed_requests", 0),
            output_requests=performance.get("output_requests", 0),
            slot_data=slot_data,
        )

        # Cache the result with specified TTL
        self._set_cache(cache_key, self._model_status_to_dict(model_status), ttl=cache_ttl)

        return model_status

    def get_multiple_models_status(
        self,
        model_names: List[str],
        time_window: str = DEFAULT_TIME_WINDOW,
        use_cache: bool = True
    ) -> List[ModelStatus]:
        """
        Get status for multiple models using batch query.

        Optimized to use a single SQL query instead of N queries.

        Args:
            model_names: List of model names to query.
            time_window: Time window ('15m', '30m', '1h', '6h', '12h', '24h').
            use_cache: Whether to use cached data.

        Returns:
            List of ModelStatus objects.
        """
        if not model_names:
            return []

        # Validate time window
        if time_window not in TIME_WINDOWS:
            time_window = DEFAULT_TIME_WINDOW

        # Check cache first for all models
        results = []
        models_to_query = []
        cached_results = {}

        if use_cache:
            for model_name in model_names:
                cache_key = f"model_status:{model_name}:{time_window}"
                cached = self._get_cache(cache_key)
                if cached:
                    cached_results[model_name] = self._dict_to_model_status(cached)
                else:
                    models_to_query.append(model_name)
        else:
            models_to_query = list(model_names)

        # If all from cache, return immediately
        if not models_to_query:
            return [cached_results[name] for name in model_names if name in cached_results]

        # Batch query for uncached models
        now = int(time.time())
        total_seconds, num_slots, slot_seconds = get_time_window_config(time_window)
        window_start = now - total_seconds

        # Build parameterized query with model list
        # Use numbered placeholders for model names
        model_placeholders = ", ".join([f":model_{i}" for i in range(len(models_to_query))])

        sql = f"""
            SELECT
                model_name,
                FLOOR((created_at - :window_start) / :slot_seconds) as slot_idx,
                COUNT(*) as total,
                SUM(CASE WHEN type = :type_success THEN 1 ELSE 0 END) as success
            FROM logs
            WHERE model_name IN ({model_placeholders})
              AND created_at >= :window_start
              AND created_at < :now
              AND type IN (:type_success, :type_failure)
            GROUP BY model_name, FLOOR((created_at - :window_start) / :slot_seconds)
        """

        # Build parameters
        params = {
            "window_start": window_start,
            "now": now,
            "slot_seconds": slot_seconds,
            "type_success": LOG_TYPE_CONSUMPTION,
            "type_failure": LOG_TYPE_FAILURE,
        }
        for i, model_name in enumerate(models_to_query):
            params[f"model_{i}"] = model_name

        # Initialize slot data for all models
        model_slot_data: Dict[str, Dict[int, Dict]] = {}
        for model_name in models_to_query:
            model_slot_data[model_name] = {}
            for i in range(num_slots):
                slot_start = window_start + (i * slot_seconds)
                slot_end = slot_start + slot_seconds
                model_slot_data[model_name][i] = {
                    'slot': i,
                    'start_time': slot_start,
                    'end_time': slot_end,
                    'total_requests': 0,
                    'success_count': 0,
                }

        # Execute batch query
        try:
            self._db.connect()
            result = self._db.execute(sql, params)

            for row in result:
                model_name = row.get("model_name")
                slot_idx = int(row.get("slot_idx") or 0)
                if model_name in model_slot_data and 0 <= slot_idx < num_slots:
                    model_slot_data[model_name][slot_idx]['total_requests'] = int(row.get("total") or 0)
                    model_slot_data[model_name][slot_idx]['success_count'] = int(row.get("success") or 0)

        except Exception as e:
            logger.db_error(f"批量获取模型状态失败: {e}")

        performance_map = self._get_model_performance_map(models_to_query, window_start, now)

        # Build ModelStatus for each model
        queried_results = {}
        for model_name in models_to_query:
            slot_data = []
            total_requests = 0
            total_success = 0

            for i in range(num_slots):
                slot_info = model_slot_data[model_name][i]
                slot_total = slot_info['total_requests']
                slot_success = slot_info['success_count']
                success_rate = (slot_success / slot_total * 100) if slot_total > 0 else 100.0
                status = get_status_color(success_rate, slot_total)

                slot_data.append(SlotStatus(
                    slot=i,
                    start_time=slot_info['start_time'],
                    end_time=slot_info['end_time'],
                    total_requests=slot_total,
                    success_count=slot_success,
                    success_rate=round(success_rate, 2),
                    status=status,
                ))

                total_requests += slot_total
                total_success += slot_success

            overall_rate = (total_success / total_requests * 100) if total_requests > 0 else 100.0
            current_status = get_status_color(overall_rate, total_requests)

            model_status = ModelStatus(
                model_name=model_name,
                display_name=self._get_display_name(model_name),
                time_window=time_window,
                total_requests=total_requests,
                success_count=total_success,
                success_rate=round(overall_rate, 2),
                current_status=current_status,
                within_5s_rate=performance_map.get(model_name, {}).get("within_5s_rate"),
                within_10s_rate=performance_map.get(model_name, {}).get("within_10s_rate"),
                duration_within_10s_rate=performance_map.get(model_name, {}).get("duration_within_10s_rate"),
                duration_within_20s_rate=performance_map.get(model_name, {}).get("duration_within_20s_rate"),
                cache_hit_rate=performance_map.get(model_name, {}).get("cache_hit_rate"),
                completion_tps=performance_map.get(model_name, {}).get("completion_tps"),
                timed_requests=performance_map.get(model_name, {}).get("timed_requests", 0),
                duration_timed_requests=performance_map.get(model_name, {}).get("duration_timed_requests", 0),
                output_requests=performance_map.get(model_name, {}).get("output_requests", 0),
                slot_data=slot_data,
            )

            # Cache the result
            cache_key = f"model_status:{model_name}:{time_window}"
            self._set_cache(cache_key, self._model_status_to_dict(model_status), ttl=CACHE_TTL_SHORT)
            queried_results[model_name] = model_status

        # Merge cached and queried results, preserving original order
        for model_name in model_names:
            if model_name in cached_results:
                results.append(cached_results[model_name])
            elif model_name in queried_results:
                results.append(queried_results[model_name])

        return results

    def get_all_models_status(self, time_window: str = DEFAULT_TIME_WINDOW, use_cache: bool = True) -> List[ModelStatus]:
        """
        Get status for all available models.
        
        Args:
            time_window: Time window ('15m', '30m', '1h', '6h', '12h', '24h').
            use_cache: Whether to use cached data.
            
        Returns:
            List of ModelStatus objects for all models.
        """
        models = self.get_available_models()
        return self.get_multiple_models_status(models, time_window, use_cache)

    def _get_display_name(self, model_name: str) -> str:
        """Get a display-friendly name for the model."""
        # Simple transformation - can be extended with a mapping table
        return model_name.replace("-", " ").replace("_", " ").title()

    def _model_status_to_dict(self, status: ModelStatus) -> Dict:
        """Convert ModelStatus to dictionary for caching."""
        return {
            "model_name": status.model_name,
            "display_name": status.display_name,
            "time_window": status.time_window,
            "total_requests": status.total_requests,
            "success_count": status.success_count,
            "success_rate": status.success_rate,
            "current_status": status.current_status,
            "within_5s_rate": status.within_5s_rate,
            "within_10s_rate": status.within_10s_rate,
            "duration_within_10s_rate": status.duration_within_10s_rate,
            "duration_within_20s_rate": status.duration_within_20s_rate,
            "cache_hit_rate": status.cache_hit_rate,
            "completion_tps": status.completion_tps,
            "timed_requests": status.timed_requests,
            "duration_timed_requests": status.duration_timed_requests,
            "output_requests": status.output_requests,
            "slot_data": [asdict(h) for h in status.slot_data],
        }

    def _dict_to_model_status(self, data: Dict) -> ModelStatus:
        """Convert dictionary to ModelStatus."""
        slot_data = [
            SlotStatus(**h) for h in data.get("slot_data", [])
        ]
        return ModelStatus(
            model_name=data["model_name"],
            display_name=data["display_name"],
            time_window=data.get("time_window", DEFAULT_TIME_WINDOW),
            total_requests=data.get("total_requests", data.get("total_requests_24h", 0)),
            success_count=data.get("success_count", data.get("success_count_24h", 0)),
            success_rate=data.get("success_rate", data.get("success_rate_24h", 0)),
            current_status=data["current_status"],
            within_5s_rate=data.get("within_5s_rate"),
            within_10s_rate=data.get("within_10s_rate"),
            duration_within_10s_rate=data.get("duration_within_10s_rate"),
            duration_within_20s_rate=data.get("duration_within_20s_rate"),
            cache_hit_rate=data.get("cache_hit_rate"),
            completion_tps=data.get("completion_tps"),
            timed_requests=data.get("timed_requests", 0),
            duration_timed_requests=data.get("duration_timed_requests", 0),
            output_requests=data.get("output_requests", 0),
            slot_data=slot_data,
        )


# Singleton instance
_model_status_service: Optional[ModelStatusService] = None


def get_model_status_service() -> ModelStatusService:
    """Get singleton instance of ModelStatusService."""
    global _model_status_service
    if _model_status_service is None:
        _model_status_service = ModelStatusService()
    return _model_status_service


async def warmup_model_status(max_models: int = 0) -> Dict[str, Any]:
    """
    Warmup model status data for faster frontend loading.
    Only warms up models with requests in the last 24 hours.
    Warms up ALL time windows (1h, 6h, 12h, 24h) using batch queries.

    Optimized strategy:
    - Uses batch query (get_multiple_models_status) instead of individual queries
    - 4 SQL queries total (one per time window) instead of N*4 queries
    - Much faster warmup (seconds instead of minutes)

    Args:
        max_models: Maximum number of models to warmup (0 = all active models).

    Returns:
        Warmup result with success count and timing.
    """
    import asyncio

    service = get_model_status_service()
    start_time = time.time()

    # First, warmup available_models_with_stats (for model selector sorting)
    models_with_stats = service.get_available_models_with_stats(use_cache=False)
    # Only warmup models with requests in the last 24 hours
    active_models = [m["model_name"] for m in models_with_stats if m["request_count_24h"] > 0]
    logger.info(f"[模型状态] 模型统计预热完成: {len(models_with_stats)} 个模型, {len(active_models)} 个有请求")

    # Only warmup active models (those with requests in last 24h)
    models_to_warmup = active_models[:max_models] if max_models > 0 else active_models

    if not models_to_warmup:
        logger.info("[模型状态] 无可用模型，跳过预热")
        return {
            "success": True,
            "models_warmed": 0,
            "windows_warmed": 0,
            "elapsed": 0,
        }

    # Get all time windows
    time_windows = list(TIME_WINDOWS.keys())  # ['1h', '6h', '12h', '24h']

    # Batch size for each query to balance between speed and database load
    # 20 models per batch is a good balance
    BATCH_SIZE = 20

    total_batches = (len(models_to_warmup) + BATCH_SIZE - 1) // BATCH_SIZE
    logger.info(f"[模型状态] 开始批量预热 {len(models_to_warmup)} 个模型 × {len(time_windows)} 个时间窗口 (每批 {BATCH_SIZE} 个)")

    success_count = 0
    failed_windows = []

    # Batch warmup: process models in chunks per time window
    for window in time_windows:
        window_start = time.time()
        window_success = 0

        try:
            # Process models in batches to avoid overwhelming the database
            for batch_idx in range(0, len(models_to_warmup), BATCH_SIZE):
                batch_models = models_to_warmup[batch_idx:batch_idx + BATCH_SIZE]

                # Use batch query for this chunk
                results = service.get_multiple_models_status(
                    model_names=batch_models,
                    time_window=window,
                    use_cache=False  # Force refresh
                )
                window_success += len(results)

                # Small delay between batches to reduce database pressure
                if batch_idx + BATCH_SIZE < len(models_to_warmup):
                    await asyncio.sleep(0.1)

            success_count += window_success
            window_elapsed = time.time() - window_start
            logger.info(f"[模型状态] 预热 {window} 窗口完成: {window_success} 个模型, 耗时 {window_elapsed:.2f}s")

            # Delay between windows
            await asyncio.sleep(0.3)

        except Exception as e:
            logger.warn(f"[模型状态] 预热 {window} 窗口失败: {e}")
            failed_windows.append(window)

    elapsed = time.time() - start_time
    total_cached = len(models_to_warmup) * (len(time_windows) - len(failed_windows))

    if failed_windows:
        logger.warn(f"[模型状态] 预热完成，成功 {success_count} 个缓存，失败窗口: {failed_windows}，耗时 {elapsed:.1f}s")
    else:
        logger.info(f"[模型状态] 预热完成: {len(models_to_warmup)} 模型 × {len(time_windows)} 窗口 = {success_count} 缓存，耗时 {elapsed:.1f}s")

    return {
        "success": True,
        "models_warmed": len(models_to_warmup),
        "windows_warmed": len(time_windows) - len(failed_windows),
        "total_cached": success_count,
        "failed": len(failed_windows),
        "elapsed": round(elapsed, 2),
    }
