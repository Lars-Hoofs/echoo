-- +goose Up
-- Knowledge base ("Kennisbank"): one public help center per installation, served without login
-- under /hulp. The portal is a singleton row; the constraint on `singleton` keeps it at one.
CREATE TABLE kb_portal (
    singleton     boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    name          text        NOT NULL DEFAULT 'Kennisbank' CHECK (length(name) BETWEEN 1 AND 100),
    title         text        NOT NULL DEFAULT 'Hoe kunnen we helpen?' CHECK (length(title) BETWEEN 1 AND 150),
    intro         text        NOT NULL DEFAULT '' CHECK (length(intro) <= 500),
    logo_text     text        NOT NULL DEFAULT '' CHECK (length(logo_text) <= 40),
    -- Host name of a reverse proxy that serves /hulp on its own domain; only used to build
    -- canonical and sitemap URLs. Empty means the base URL of Echoo.
    custom_domain text        NOT NULL DEFAULT '' CHECK (length(custom_domain) <= 253
        AND custom_domain ~ '^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+)?$'),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
INSERT INTO kb_portal (singleton) VALUES (true);

-- One level of nesting: the API refuses a parent that has a parent itself.
CREATE TABLE kb_categories (
    id          uuid PRIMARY KEY DEFAULT uuidv7(),
    parent_id   uuid REFERENCES kb_categories (id) ON DELETE RESTRICT,
    name        text        NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    slug        text        NOT NULL UNIQUE CHECK (length(slug) <= 80 AND slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$'),
    description text        NOT NULL DEFAULT '' CHECK (length(description) <= 300),
    position    integer     NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CHECK (parent_id IS DISTINCT FROM id)
);
CREATE INDEX kb_categories_parent ON kb_categories (parent_id);

-- body_text is derived from the sanitized body_html by the server; it feeds search and excerpts.
-- Search indexes the stemmed Dutch text and the unstemmed simple text, like message search.
CREATE TABLE kb_articles (
    id           uuid PRIMARY KEY DEFAULT uuidv7(),
    category_id  uuid REFERENCES kb_categories (id) ON DELETE RESTRICT,
    title        text        NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
    slug         text        NOT NULL UNIQUE CHECK (length(slug) <= 80 AND slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$'),
    body_html    text        NOT NULL DEFAULT '',
    body_text    text        NOT NULL DEFAULT '',
    excerpt      text        NOT NULL DEFAULT '' CHECK (length(excerpt) <= 300),
    status       text        NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'published', 'archived')),
    author_id    uuid REFERENCES users (id) ON DELETE SET NULL,
    updated_by   uuid REFERENCES users (id) ON DELETE SET NULL,
    version      integer     NOT NULL DEFAULT 1,
    position     integer     NOT NULL DEFAULT 0,
    view_count   bigint      NOT NULL DEFAULT 0,
    helpful_yes  integer     NOT NULL DEFAULT 0,
    helpful_no   integer     NOT NULL DEFAULT 0,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    published_at timestamptz,
    fts tsvector GENERATED ALWAYS AS (
        setweight(to_tsvector('dutch', title), 'A') || setweight(to_tsvector('echoo_simple', title), 'A') ||
        setweight(to_tsvector('dutch', excerpt), 'B') ||
        setweight(to_tsvector('dutch', body_text), 'C') || setweight(to_tsvector('echoo_simple', body_text), 'D')
    ) STORED,
    CHECK (status <> 'published' OR category_id IS NOT NULL)
);
CREATE INDEX kb_articles_fts ON kb_articles USING gin (fts) WHERE status = 'published';
CREATE INDEX kb_articles_category ON kb_articles (category_id, position, title);
CREATE INDEX kb_articles_status ON kb_articles (status, updated_at DESC);

-- The previous state of an article, written before each content change; the API keeps 20.
CREATE TABLE kb_article_revisions (
    id         uuid PRIMARY KEY DEFAULT uuidv7(),
    article_id uuid        NOT NULL REFERENCES kb_articles (id) ON DELETE CASCADE,
    title      text        NOT NULL,
    excerpt    text        NOT NULL,
    body_html  text        NOT NULL,
    edited_by  uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX kb_article_revisions_article ON kb_article_revisions (article_id, created_at DESC, id DESC);

-- A slug an article used to have keeps working as a 301 to the current one.
CREATE TABLE kb_slug_redirects (
    slug       text PRIMARY KEY CHECK (slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$'),
    article_id uuid        NOT NULL REFERENCES kb_articles (id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX kb_slug_redirects_article ON kb_slug_redirects (article_id);

CREATE TABLE kb_images (
    id           uuid PRIMARY KEY DEFAULT uuidv7(),
    uploader_id  uuid REFERENCES users (id) ON DELETE SET NULL,
    filename     text        NOT NULL,
    content_type text        NOT NULL,
    size_bytes   bigint      NOT NULL CHECK (size_bytes > 0),
    blob_key     text        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);

-- Which images a saved article references. An image nobody references is removed a day after
-- its upload; one referenced by a published article is served publicly.
CREATE TABLE kb_article_images (
    article_id uuid NOT NULL REFERENCES kb_articles (id) ON DELETE CASCADE,
    image_id   uuid NOT NULL REFERENCES kb_images (id) ON DELETE CASCADE,
    PRIMARY KEY (article_id, image_id)
);
CREATE INDEX kb_article_images_image ON kb_article_images (image_id);

-- +goose Down
DROP TABLE kb_article_images;
DROP TABLE kb_images;
DROP TABLE kb_slug_redirects;
DROP TABLE kb_article_revisions;
DROP TABLE kb_articles;
DROP TABLE kb_categories;
DROP TABLE kb_portal;
