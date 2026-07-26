CREATE TABLE knowledge_bases (
    id varchar(64) PRIMARY KEY,
    name varchar(255) NOT NULL,
    description text NOT NULL DEFAULT '',
    status varchar(20) NOT NULL DEFAULT 'active',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT knowledge_bases_name_not_blank CHECK (btrim(name) <> ''),
    CONSTRAINT knowledge_bases_status_valid CHECK (status IN ('active', 'disabled'))
);

CREATE TABLE knowledge_documents (
    id varchar(64) PRIMARY KEY,
    knowledge_base_id varchar(64) NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    document_type varchar(20) NOT NULL,
    title varchar(500) NOT NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    active_version_id varchar(64),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz,
    CONSTRAINT knowledge_documents_type_valid CHECK (document_type IN ('faq', 'markdown')),
    CONSTRAINT knowledge_documents_title_not_blank CHECK (btrim(title) <> ''),
    CONSTRAINT knowledge_documents_metadata_object CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE TABLE knowledge_document_versions (
    id varchar(64) PRIMARY KEY,
    document_id varchar(64) NOT NULL REFERENCES knowledge_documents(id) ON DELETE CASCADE,
    version integer NOT NULL,
    content text NOT NULL,
    content_sha256 char(64) NOT NULL,
    index_status varchar(20) NOT NULL DEFAULT 'pending',
    embedding_provider varchar(100) NOT NULL,
    embedding_model varchar(100) NOT NULL,
    embedding_dimensions integer NOT NULL,
    index_error text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    indexed_at timestamptz,
    CONSTRAINT knowledge_document_versions_number_positive CHECK (version > 0),
    CONSTRAINT knowledge_document_versions_content_not_blank CHECK (btrim(content) <> ''),
    CONSTRAINT knowledge_document_versions_checksum_valid CHECK (
        content_sha256 ~ '^[0-9a-f]{64}$'
    ),
    CONSTRAINT knowledge_document_versions_provider_not_blank CHECK (
        btrim(embedding_provider) <> ''
    ),
    CONSTRAINT knowledge_document_versions_model_not_blank CHECK (
        btrim(embedding_model) <> ''
    ),
    CONSTRAINT knowledge_document_versions_dimensions_valid CHECK (
        embedding_dimensions = 1024
    ),
    CONSTRAINT knowledge_document_versions_status_valid CHECK (
        index_status IN ('pending', 'indexing', 'ready', 'failed')
    ),
    CONSTRAINT knowledge_document_versions_status_fields_consistent CHECK (
        (
            index_status IN ('pending', 'indexing')
            AND indexed_at IS NULL
            AND index_error = ''
        )
        OR
        (
            index_status = 'ready'
            AND indexed_at IS NOT NULL
            AND index_error = ''
        )
        OR
        (
            index_status = 'failed'
            AND indexed_at IS NULL
            AND btrim(index_error) <> ''
        )
    ),
    CONSTRAINT knowledge_document_versions_document_version_unique UNIQUE (document_id, version),
    CONSTRAINT knowledge_document_versions_document_id_unique UNIQUE (document_id, id)
);

ALTER TABLE knowledge_documents
    ADD CONSTRAINT knowledge_documents_active_version_same_document
    FOREIGN KEY (id, active_version_id)
    REFERENCES knowledge_document_versions(document_id, id)
    ON DELETE RESTRICT;

CREATE TABLE knowledge_chunks (
    id varchar(64) PRIMARY KEY,
    version_id varchar(64) NOT NULL REFERENCES knowledge_document_versions(id) ON DELETE CASCADE,
    position integer NOT NULL,
    content text NOT NULL,
    token_count integer NOT NULL DEFAULT 0,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    embedding vector(1024) NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT knowledge_chunks_position_non_negative CHECK (position >= 0),
    CONSTRAINT knowledge_chunks_content_not_blank CHECK (btrim(content) <> ''),
    CONSTRAINT knowledge_chunks_token_count_non_negative CHECK (token_count >= 0),
    CONSTRAINT knowledge_chunks_metadata_object CHECK (jsonb_typeof(metadata) = 'object'),
    CONSTRAINT knowledge_chunks_version_position_unique UNIQUE (version_id, position)
);

CREATE FUNCTION enforce_ready_active_knowledge_version()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.active_version_id IS NOT NULL
       AND NOT EXISTS (
           SELECT 1
           FROM knowledge_document_versions
           WHERE id = NEW.active_version_id
             AND document_id = NEW.id
             AND index_status = 'ready'
       )
    THEN
        RAISE EXCEPTION 'active knowledge version must belong to the document and be ready'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER knowledge_documents_active_version_ready
BEFORE INSERT OR UPDATE OF active_version_id
ON knowledge_documents
FOR EACH ROW
EXECUTE FUNCTION enforce_ready_active_knowledge_version();

CREATE INDEX knowledge_documents_base_active_index
    ON knowledge_documents (knowledge_base_id, active_version_id)
    WHERE deleted_at IS NULL;

CREATE INDEX knowledge_document_versions_pending_index
    ON knowledge_document_versions (created_at)
    WHERE index_status IN ('pending', 'indexing', 'failed');

CREATE INDEX knowledge_chunks_version_index
    ON knowledge_chunks (version_id, position);

CREATE INDEX knowledge_chunks_metadata_index
    ON knowledge_chunks USING gin (metadata jsonb_path_ops);

CREATE INDEX knowledge_chunks_embedding_hnsw_index
    ON knowledge_chunks USING hnsw (embedding vector_cosine_ops);
