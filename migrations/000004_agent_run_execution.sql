ALTER TABLE messages
    ADD COLUMN agent_run_id varchar(64);

ALTER TABLE messages
    ADD CONSTRAINT messages_agent_run_reference
    FOREIGN KEY (agent_run_id)
    REFERENCES agent_runs(id)
    ON DELETE RESTRICT;

ALTER TABLE messages
    ADD CONSTRAINT messages_agent_run_role_consistent CHECK (
        (
            role = 'assistant'
            AND agent_run_id IS NOT NULL
        )
        OR
        (
            role <> 'assistant'
            AND agent_run_id IS NULL
        )
    );

CREATE UNIQUE INDEX messages_agent_run_unique
    ON messages (agent_run_id)
    WHERE agent_run_id IS NOT NULL;
