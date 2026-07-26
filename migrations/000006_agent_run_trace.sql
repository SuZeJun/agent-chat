ALTER TABLE agent_runs
    ADD COLUMN request_id varchar(64);

UPDATE agent_runs
SET request_id = 'legacy_' || substr(md5(id), 1, 32)
WHERE request_id IS NULL;

ALTER TABLE agent_runs
    ALTER COLUMN request_id SET NOT NULL;

ALTER TABLE agent_runs
    ADD CONSTRAINT agent_runs_request_id_not_blank CHECK (
        btrim(request_id) <> ''
    );

CREATE INDEX agent_runs_request_id_index
    ON agent_runs (request_id);

CREATE TABLE agent_run_steps (
    id bigserial PRIMARY KEY,
    run_id varchar(64) NOT NULL
        REFERENCES agent_runs(id)
        ON DELETE CASCADE,
    step_order integer NOT NULL,
    name varchar(100) NOT NULL,
    component varchar(100) NOT NULL,
    component_type varchar(255) NOT NULL DEFAULT '',
    status varchar(20) NOT NULL,
    started_at timestamptz NOT NULL,
    completed_at timestamptz NOT NULL,
    duration_ms bigint NOT NULL,
    prompt_tokens integer NOT NULL DEFAULT 0,
    completion_tokens integer NOT NULL DEFAULT 0,
    CONSTRAINT agent_run_steps_order_positive CHECK (step_order > 0),
    CONSTRAINT agent_run_steps_name_not_blank CHECK (btrim(name) <> ''),
    CONSTRAINT agent_run_steps_component_not_blank CHECK (btrim(component) <> ''),
    CONSTRAINT agent_run_steps_status_valid CHECK (
        status IN ('completed', 'failed')
    ),
    CONSTRAINT agent_run_steps_timestamps_ordered CHECK (
        completed_at >= started_at
    ),
    CONSTRAINT agent_run_steps_metrics_non_negative CHECK (
        duration_ms >= 0
        AND prompt_tokens >= 0
        AND completion_tokens >= 0
    ),
    CONSTRAINT agent_run_steps_run_order_unique UNIQUE (run_id, step_order)
);

CREATE INDEX agent_run_steps_run_index
    ON agent_run_steps (run_id, step_order);
