CREATE TABLE conversations (
    id varchar(64) PRIMARY KEY,
    customer_id varchar(64) NOT NULL,
    knowledge_base_id varchar(64) NOT NULL REFERENCES knowledge_bases(id) ON DELETE RESTRICT,
    status varchar(30) NOT NULL DEFAULT 'ai_active',
    last_message_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT conversations_customer_not_blank CHECK (btrim(customer_id) <> ''),
    CONSTRAINT conversations_status_valid CHECK (
        status IN ('ai_active', 'waiting_human', 'human_active', 'closed')
    ),
    CONSTRAINT conversations_timestamps_ordered CHECK (updated_at >= created_at),
    CONSTRAINT conversations_last_message_ordered CHECK (
        last_message_at IS NULL OR last_message_at >= created_at
    )
);

CREATE TABLE messages (
    id varchar(64) PRIMARY KEY,
    conversation_id varchar(64) NOT NULL REFERENCES conversations(id) ON DELETE RESTRICT,
    client_message_id varchar(100) NOT NULL DEFAULT '',
    role varchar(20) NOT NULL,
    content text NOT NULL,
    created_at timestamptz NOT NULL,
    CONSTRAINT messages_role_valid CHECK (
        role IN ('customer', 'assistant', 'agent', 'system')
    ),
    CONSTRAINT messages_content_not_blank CHECK (btrim(content) <> ''),
    CONSTRAINT messages_client_id_role_consistent CHECK (
        (
            role = 'customer'
            AND btrim(client_message_id) <> ''
        )
        OR
        (
            role <> 'customer'
            AND client_message_id = ''
        )
    ),
    CONSTRAINT messages_conversation_id_unique UNIQUE (conversation_id, id)
);

CREATE UNIQUE INDEX messages_client_id_unique
    ON messages (conversation_id, client_message_id)
    WHERE client_message_id <> '';

CREATE INDEX messages_conversation_created_index
    ON messages (conversation_id, created_at, id);

CREATE TABLE agent_runs (
    id varchar(64) PRIMARY KEY,
    conversation_id varchar(64) NOT NULL,
    source_message_id varchar(64) NOT NULL UNIQUE,
    status varchar(30) NOT NULL DEFAULT 'pending',
    result jsonb NOT NULL DEFAULT '{}'::jsonb,
    error_code varchar(100) NOT NULL DEFAULT '',
    started_at timestamptz,
    completed_at timestamptz,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT agent_runs_source_message_same_conversation
        FOREIGN KEY (conversation_id, source_message_id)
        REFERENCES messages(conversation_id, id)
        ON DELETE RESTRICT,
    CONSTRAINT agent_runs_status_valid CHECK (
        status IN ('pending', 'running', 'completed', 'failed')
    ),
    CONSTRAINT agent_runs_result_object CHECK (jsonb_typeof(result) = 'object'),
    CONSTRAINT agent_runs_timestamps_ordered CHECK (
        updated_at >= created_at
        AND (started_at IS NULL OR started_at >= created_at)
        AND (completed_at IS NULL OR completed_at >= created_at)
        AND (
            started_at IS NULL
            OR completed_at IS NULL
            OR completed_at >= started_at
        )
    ),
    CONSTRAINT agent_runs_status_fields_consistent CHECK (
        (
            status = 'pending'
            AND started_at IS NULL
            AND completed_at IS NULL
            AND error_code = ''
        )
        OR
        (
            status = 'running'
            AND started_at IS NOT NULL
            AND completed_at IS NULL
            AND error_code = ''
        )
        OR
        (
            status = 'completed'
            AND started_at IS NOT NULL
            AND completed_at IS NOT NULL
            AND error_code = ''
        )
        OR
        (
            status = 'failed'
            AND started_at IS NOT NULL
            AND completed_at IS NOT NULL
            AND btrim(error_code) <> ''
        )
    )
);

CREATE INDEX agent_runs_conversation_created_index
    ON agent_runs (conversation_id, created_at, id);

CREATE INDEX agent_runs_active_index
    ON agent_runs (created_at)
    WHERE status IN ('pending', 'running');

CREATE TABLE run_events (
    id varchar(64) PRIMARY KEY,
    run_id varchar(64) NOT NULL REFERENCES agent_runs(id) ON DELETE RESTRICT,
    sequence integer NOT NULL,
    event_type varchar(50) NOT NULL,
    payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL,
    CONSTRAINT run_events_sequence_positive CHECK (sequence > 0),
    CONSTRAINT run_events_type_valid CHECK (
        event_type IN (
            'run.started',
            'run.status',
            'retrieval.completed',
            'answerability.decided',
            'message.delta',
            'message.citation',
            'run.completed',
            'run.failed'
        )
    ),
    CONSTRAINT run_events_payload_object CHECK (jsonb_typeof(payload) = 'object'),
    CONSTRAINT run_events_run_sequence_unique UNIQUE (run_id, sequence)
);
