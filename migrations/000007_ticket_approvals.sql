CREATE TABLE ticket_approvals (
    id varchar(64) PRIMARY KEY,
    conversation_id varchar(64) NOT NULL
        REFERENCES conversations(id)
        ON DELETE RESTRICT,
    customer_id varchar(64) NOT NULL,
    agent_run_id varchar(64) NOT NULL
        REFERENCES agent_runs(id)
        ON DELETE RESTRICT,
    title varchar(120) NOT NULL,
    description text NOT NULL,
    priority varchar(10) NOT NULL,
    status varchar(20) NOT NULL DEFAULT 'pending',
    idempotency_key varchar(64) NOT NULL UNIQUE,
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    decided_at timestamptz,
    CONSTRAINT ticket_approvals_customer_not_blank CHECK (btrim(customer_id) <> ''),
    CONSTRAINT ticket_approvals_title_not_blank CHECK (btrim(title) <> ''),
    CONSTRAINT ticket_approvals_description_not_blank CHECK (btrim(description) <> ''),
    CONSTRAINT ticket_approvals_priority_valid CHECK (
        priority IN ('low', 'normal', 'high')
    ),
    CONSTRAINT ticket_approvals_status_valid CHECK (
        status IN ('pending', 'approved', 'cancelled', 'expired')
    ),
    CONSTRAINT ticket_approvals_expiry_ordered CHECK (expires_at > created_at),
    -- 终态必须带决策时间，pending 必须没有：避免出现"已决策但不知何时"的记录。
    CONSTRAINT ticket_approvals_decision_consistent CHECK (
        (status = 'pending' AND decided_at IS NULL)
        OR (status <> 'pending' AND decided_at IS NOT NULL)
    ),
    -- 一次 Agent Run 至多产生一个待确认请求。重复发起会撞上该约束而不是
    -- 悄悄创建第二个审批，否则同一次运行可能产生两张工单。
    CONSTRAINT ticket_approvals_run_unique UNIQUE (agent_run_id)
);

CREATE INDEX ticket_approvals_conversation_index
    ON ticket_approvals (conversation_id, created_at DESC);

CREATE INDEX ticket_approvals_pending_index
    ON ticket_approvals (expires_at)
    WHERE status = 'pending';

CREATE TABLE tickets (
    id varchar(64) PRIMARY KEY,
    number varchar(32) NOT NULL UNIQUE,
    conversation_id varchar(64) NOT NULL
        REFERENCES conversations(id)
        ON DELETE RESTRICT,
    customer_id varchar(64) NOT NULL,
    -- 一个审批至多产生一张工单。这是"重复确认不产生重复副作用"的最后一道
    -- 防线：即便应用层的状态转换守卫被绕过，唯一约束仍会拒绝第二次写入。
    approval_id varchar(64) NOT NULL UNIQUE
        REFERENCES ticket_approvals(id)
        ON DELETE RESTRICT,
    title varchar(120) NOT NULL,
    description text NOT NULL,
    priority varchar(10) NOT NULL,
    created_at timestamptz NOT NULL,
    CONSTRAINT tickets_number_not_blank CHECK (btrim(number) <> ''),
    CONSTRAINT tickets_customer_not_blank CHECK (btrim(customer_id) <> ''),
    CONSTRAINT tickets_title_not_blank CHECK (btrim(title) <> ''),
    CONSTRAINT tickets_description_not_blank CHECK (btrim(description) <> ''),
    CONSTRAINT tickets_priority_valid CHECK (
        priority IN ('low', 'normal', 'high')
    )
);

CREATE INDEX tickets_customer_index
    ON tickets (customer_id, created_at DESC);
