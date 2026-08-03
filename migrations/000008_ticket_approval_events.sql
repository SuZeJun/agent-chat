ALTER TABLE run_events
    DROP CONSTRAINT run_events_type_valid;

ALTER TABLE run_events
    ADD CONSTRAINT run_events_type_valid CHECK (
        event_type IN (
            'run.started',
            'run.status',
            'retrieval.completed',
            'answerability.decided',
            'message.delta',
            'message.citation',
            'approval.required',
            'approval.confirmed',
            'approval.cancelled',
            'approval.expired',
            'ticket.created',
            'run.completed',
            'run.failed'
        )
    );
