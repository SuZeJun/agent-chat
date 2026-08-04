ALTER TABLE conversations
    ADD COLUMN assigned_agent_id varchar(64) NOT NULL DEFAULT '';

ALTER TABLE conversations
    ADD CONSTRAINT conversations_agent_assignment_consistent CHECK (
        (
            status = 'human_active'
            AND btrim(assigned_agent_id) <> ''
        )
        OR
        (
            status <> 'human_active'
            AND assigned_agent_id = ''
        )
    );

CREATE INDEX conversations_handoff_queue_index
    ON conversations (status, updated_at, id)
    WHERE status IN ('waiting_human', 'human_active');

CREATE TABLE handoff_summaries (
    conversation_id varchar(64) PRIMARY KEY
        REFERENCES conversations(id) ON DELETE RESTRICT,
    customer_request text NOT NULL,
    confirmed_facts jsonb NOT NULL DEFAULT '[]'::jsonb,
    unresolved_questions jsonb NOT NULL DEFAULT '[]'::jsonb,
    risk_signals jsonb NOT NULL DEFAULT '[]'::jsonb,
    citations jsonb NOT NULL DEFAULT '[]'::jsonb,
    tool_calls jsonb NOT NULL DEFAULT '[]'::jsonb,
    recommended_action text NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT handoff_summaries_request_not_blank CHECK (btrim(customer_request) <> ''),
    CONSTRAINT handoff_summaries_recommendation_not_blank CHECK (btrim(recommended_action) <> ''),
    CONSTRAINT handoff_summaries_arrays_valid CHECK (
        jsonb_typeof(confirmed_facts) = 'array'
        AND jsonb_typeof(unresolved_questions) = 'array'
        AND jsonb_typeof(risk_signals) = 'array'
        AND jsonb_typeof(citations) = 'array'
        AND jsonb_typeof(tool_calls) = 'array'
    ),
    CONSTRAINT handoff_summaries_timestamps_ordered CHECK (updated_at >= created_at)
);

CREATE TABLE conversation_events (
    id varchar(64) PRIMARY KEY,
    conversation_id varchar(64) NOT NULL
        REFERENCES conversations(id) ON DELETE RESTRICT,
    sequence integer NOT NULL,
    event_type varchar(50) NOT NULL,
    actor_type varchar(20) NOT NULL,
    actor_id varchar(64) NOT NULL DEFAULT '',
    payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL,
    CONSTRAINT conversation_events_sequence_positive CHECK (sequence > 0),
    CONSTRAINT conversation_events_type_valid CHECK (
        event_type IN (
            'handoff.requested',
            'handoff.taken_over',
            'message.customer',
            'message.agent',
            'handoff.ai_resumed'
        )
    ),
    CONSTRAINT conversation_events_actor_valid CHECK (
        (
            actor_type IN ('customer', 'agent')
            AND btrim(actor_id) <> ''
        )
        OR
        (
            actor_type = 'system'
            AND actor_id = ''
        )
    ),
    CONSTRAINT conversation_events_payload_object CHECK (jsonb_typeof(payload) = 'object'),
    CONSTRAINT conversation_events_sequence_unique UNIQUE (conversation_id, sequence)
);

CREATE INDEX conversation_events_cursor_index
    ON conversation_events (conversation_id, sequence);
