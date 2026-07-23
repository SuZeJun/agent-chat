CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE jobs (
    id varchar(64) PRIMARY KEY,
    job_type varchar(100) NOT NULL,
    idempotency_key varchar(255) NOT NULL DEFAULT '',
    payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    status varchar(30) NOT NULL,
    attempts integer NOT NULL DEFAULT 0,
    max_attempts integer NOT NULL DEFAULT 5,
    available_at timestamptz NOT NULL DEFAULT now(),
    locked_at timestamptz,
    locked_by varchar(100) NOT NULL DEFAULT '',
    last_error text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT jobs_status_valid CHECK (
        status IN ('pending', 'running', 'retry_wait', 'succeeded', 'failed')
    ),
    CONSTRAINT jobs_attempts_non_negative CHECK (attempts >= 0),
    CONSTRAINT jobs_max_attempts_positive CHECK (max_attempts > 0),
    CONSTRAINT jobs_attempts_within_limit CHECK (attempts <= max_attempts),
    CONSTRAINT jobs_lock_state_consistent CHECK (
        (
            status = 'running'
            AND locked_at IS NOT NULL
            AND locked_by <> ''
        )
        OR
        (
            status <> 'running'
            AND locked_at IS NULL
            AND locked_by = ''
        )
    )
);

CREATE UNIQUE INDEX jobs_idempotency_unique
    ON jobs (job_type, idempotency_key)
    WHERE idempotency_key <> '';

CREATE INDEX jobs_available_poll_index
    ON jobs (available_at, created_at)
    WHERE status IN ('pending', 'retry_wait');

CREATE INDEX jobs_running_lock_recovery_index
    ON jobs (locked_at)
    WHERE status = 'running';
