CREATE TABLE knowledge_imports (
    id varchar(64) PRIMARY KEY,
    knowledge_base_id varchar(64) NOT NULL
        REFERENCES knowledge_bases(id)
        ON DELETE CASCADE,
    source_name varchar(255) NOT NULL,
    content_sha256 char(64) NOT NULL,
    total_rows integer NOT NULL,
    created_at timestamptz NOT NULL,
    CONSTRAINT knowledge_imports_source_not_blank CHECK (
        btrim(source_name) <> ''
    ),
    CONSTRAINT knowledge_imports_checksum_valid CHECK (
        content_sha256 ~ '^[0-9a-f]{64}$'
    ),
    CONSTRAINT knowledge_imports_total_rows_valid CHECK (
        total_rows BETWEEN 1 AND 1000
    ),
    CONSTRAINT knowledge_imports_content_unique UNIQUE (
        knowledge_base_id,
        content_sha256
    )
);

CREATE TABLE knowledge_import_items (
    import_id varchar(64) NOT NULL
        REFERENCES knowledge_imports(id)
        ON DELETE CASCADE,
    row_number integer NOT NULL,
    document_id varchar(64) NOT NULL
        REFERENCES knowledge_documents(id)
        ON DELETE RESTRICT,
    version_id varchar(64) NOT NULL
        REFERENCES knowledge_document_versions(id)
        ON DELETE RESTRICT,
    job_id varchar(64) NOT NULL
        REFERENCES jobs(id)
        ON DELETE RESTRICT,
    CONSTRAINT knowledge_import_items_row_positive CHECK (
        row_number >= 2
    ),
    CONSTRAINT knowledge_import_items_primary PRIMARY KEY (
        import_id,
        row_number
    ),
    CONSTRAINT knowledge_import_items_document_unique UNIQUE (document_id),
    CONSTRAINT knowledge_import_items_version_unique UNIQUE (version_id),
    CONSTRAINT knowledge_import_items_job_unique UNIQUE (job_id)
);

CREATE INDEX knowledge_imports_base_created_index
    ON knowledge_imports (knowledge_base_id, created_at DESC, id);
