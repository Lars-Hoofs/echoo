-- Knowledge base: portal settings, categories, articles, revisions, images, and the public
-- reads behind /hulp. Public reads only ever return published articles.

-- name: KbGetPortal :one
SELECT name, title, intro, logo_text, custom_domain, updated_at FROM kb_portal;

-- name: KbUpdatePortal :one
UPDATE kb_portal
SET name = @name, title = @title, intro = @intro, logo_text = @logo_text, custom_domain = @custom_domain, updated_at = now()
RETURNING name, title, intro, logo_text, custom_domain, updated_at;

-- name: KbListCategories :many
SELECT c.id, c.parent_id, c.name, c.slug, c.description, c.position, c.updated_at,
       (SELECT count(*) FROM kb_articles a WHERE a.category_id = c.id)::integer AS article_count,
       (SELECT count(*) FROM kb_articles a WHERE a.category_id = c.id AND a.status = 'published')::integer AS published_count
FROM kb_categories c
ORDER BY c.position, c.name, c.id;

-- name: KbGetCategory :one
SELECT * FROM kb_categories WHERE id = $1;

-- name: KbLockCategory :one
SELECT * FROM kb_categories WHERE id = $1 FOR UPDATE;

-- name: KbInsertCategory :one
INSERT INTO kb_categories (parent_id, name, slug, description, position)
VALUES (sqlc.narg(parent_id), @name, @slug, @description, @position)
RETURNING *;

-- name: KbUpdateCategory :one
UPDATE kb_categories
SET parent_id = sqlc.narg(parent_id), name = @name, slug = @slug, description = @description,
    position = @position, updated_at = now()
WHERE id = @id
RETURNING *;

-- name: KbCategoryUsage :one
SELECT (SELECT count(*) FROM kb_categories WHERE parent_id = @id)::integer AS children,
       (SELECT count(*) FROM kb_articles WHERE category_id = @id)::integer AS articles;

-- name: KbDeleteCategory :execrows
DELETE FROM kb_categories WHERE id = $1;

-- name: KbInsertArticle :one
INSERT INTO kb_articles (category_id, title, slug, body_html, body_text, excerpt, author_id, updated_by)
VALUES (sqlc.narg(category_id), @title, @slug, @body_html, @body_text, @excerpt, sqlc.narg(user_id), sqlc.narg(user_id))
RETURNING *;

-- name: KbGetArticle :one
SELECT * FROM kb_articles WHERE id = $1;

-- name: KbLockArticle :one
SELECT * FROM kb_articles WHERE id = $1 FOR UPDATE;

-- name: KbUpdateArticle :one
UPDATE kb_articles
SET category_id = sqlc.narg(category_id), title = @title, slug = @slug, body_html = @body_html,
    body_text = @body_text, excerpt = @excerpt, updated_by = sqlc.narg(user_id),
    version = version + 1, updated_at = now()
WHERE id = @id
RETURNING *;

-- name: KbSetArticleStatus :one
UPDATE kb_articles
SET status = @status::text, updated_by = sqlc.narg(user_id), version = version + 1, updated_at = now(),
    published_at = CASE WHEN @status::text = 'published' AND published_at IS NULL THEN now() ELSE published_at END
WHERE id = @id
RETURNING *;

-- name: KbDeleteArticle :execrows
DELETE FROM kb_articles WHERE id = $1;

-- name: KbListArticles :many
-- Agents see every status; published_only is set for readonly users. title_pattern is an
-- ILIKE pattern the caller already escaped, so a title fragment finds drafts that full text
-- (which only stems whole words) would miss.
SELECT a.id, a.category_id, a.title, a.slug, COALESCE(NULLIF(a.excerpt, ''), left(a.body_text, 300))::text AS excerpt, a.status, a.author_id, a.updated_by, a.version,
       a.view_count, a.helpful_yes, a.helpful_no, a.created_at, a.updated_at, a.published_at,
       c.name AS category_name, c.slug AS category_slug, u.name AS updated_by_name
FROM kb_articles a
LEFT JOIN kb_categories c ON c.id = a.category_id
LEFT JOIN users u ON u.id = a.updated_by
WHERE (NOT @published_only::boolean OR a.status = 'published')
  AND (sqlc.narg(status)::text IS NULL OR a.status = sqlc.narg(status)::text)
  AND (sqlc.narg(category_id)::uuid IS NULL OR a.category_id = sqlc.narg(category_id)::uuid)
  AND (sqlc.narg(text)::text IS NULL
       OR a.title ILIKE sqlc.narg(title_pattern)::text
       OR a.fts @@ (websearch_to_tsquery('dutch', sqlc.narg(text)::text) || websearch_to_tsquery('echoo_simple', sqlc.narg(text)::text)))
ORDER BY a.updated_at DESC, a.id DESC
LIMIT @page_size::integer;

-- name: KbListRevisions :many
SELECT r.id, r.title, r.created_at, u.name AS edited_by_name
FROM kb_article_revisions r
LEFT JOIN users u ON u.id = r.edited_by
WHERE r.article_id = $1
ORDER BY r.created_at DESC, r.id DESC;

-- name: KbGetRevision :one
SELECT * FROM kb_article_revisions WHERE id = @id AND article_id = @article_id;

-- name: KbInsertRevision :exec
INSERT INTO kb_article_revisions (article_id, title, excerpt, body_html, edited_by)
SELECT a.id, a.title, a.excerpt, a.body_html, sqlc.narg(user_id) FROM kb_articles a WHERE a.id = @id;

-- name: KbTrimRevisions :exec
DELETE FROM kb_article_revisions AS rev
WHERE rev.article_id = @article_id
  AND rev.id NOT IN (SELECT r.id FROM kb_article_revisions r WHERE r.article_id = @article_id
                 ORDER BY r.created_at DESC, r.id DESC LIMIT @keep::integer);

-- name: KbInsertRedirect :exec
INSERT INTO kb_slug_redirects (slug, article_id) VALUES (@slug, @article_id)
ON CONFLICT (slug) DO UPDATE SET article_id = EXCLUDED.article_id;

-- name: KbDeleteRedirect :exec
DELETE FROM kb_slug_redirects WHERE slug = $1;

-- name: KbFindRedirect :one
SELECT a.slug FROM kb_slug_redirects r JOIN kb_articles a ON a.id = r.article_id
WHERE r.slug = $1 AND a.status = 'published';

-- name: KbSlugOwner :one
-- The article that uses a slug now or used to (its redirect keeps the old link alive).
SELECT a.id AS article_id FROM kb_articles a WHERE a.slug = @slug
UNION ALL
SELECT r.article_id FROM kb_slug_redirects r WHERE r.slug = @slug
LIMIT 1;

-- name: KbExistingImages :many
SELECT id FROM kb_images WHERE id = ANY (@ids::uuid[]);

-- name: KbInsertImage :one
INSERT INTO kb_images (uploader_id, filename, content_type, size_bytes, blob_key)
VALUES (@uploader_id, @filename, @content_type, @size_bytes, @blob_key)
RETURNING *;

-- name: KbGetImage :one
SELECT * FROM kb_images WHERE id = $1;

-- name: KbImageIsPublished :one
SELECT EXISTS (
    SELECT 1 FROM kb_article_images ai JOIN kb_articles a ON a.id = ai.article_id
    WHERE ai.image_id = $1 AND a.status = 'published'
)::boolean;

-- name: KbDeleteUnusedImages :exec
DELETE FROM kb_images
WHERE created_at < now() - interval '24 hours'
  AND NOT EXISTS (SELECT 1 FROM kb_article_images ai WHERE ai.image_id = kb_images.id);

-- name: KbClearArticleImages :exec
DELETE FROM kb_article_images WHERE article_id = $1;

-- name: KbSetArticleImages :exec
INSERT INTO kb_article_images (article_id, image_id)
SELECT @article_id, unnest(@image_ids::uuid[])
ON CONFLICT DO NOTHING;

-- name: KbPublicCategories :many
-- Top-level categories that hold at least one published article, counting their children's.
SELECT c.id, c.name, c.slug, c.description,
       (SELECT count(*) FROM kb_articles a
        WHERE a.status = 'published' AND (a.category_id = c.id OR a.category_id IN (SELECT ch.id FROM kb_categories ch WHERE ch.parent_id = c.id)))::integer AS article_count
FROM kb_categories c
WHERE c.parent_id IS NULL
ORDER BY c.position, c.name, c.id;

-- name: KbPublicCategoryBySlug :one
SELECT c.id, c.parent_id, c.name, c.slug, c.description, c.updated_at,
       p.name AS parent_name, p.slug AS parent_slug
FROM kb_categories c
LEFT JOIN kb_categories p ON p.id = c.parent_id
WHERE c.slug = $1;

-- name: KbPublicChildCategories :many
SELECT c.id, c.name, c.slug, c.description,
       (SELECT count(*) FROM kb_articles a WHERE a.category_id = c.id AND a.status = 'published')::integer AS article_count
FROM kb_categories c
WHERE c.parent_id = $1
ORDER BY c.position, c.name, c.id;

-- name: KbPublicCategoryArticles :many
SELECT a.id, a.title, a.slug, COALESCE(NULLIF(a.excerpt, ''), left(a.body_text, 300))::text AS excerpt, a.updated_at
FROM kb_articles a
WHERE a.category_id = $1 AND a.status = 'published'
ORDER BY a.position, a.title, a.id;

-- name: KbPublicArticleBySlug :one
SELECT a.id, a.title, a.slug, a.body_html, COALESCE(NULLIF(a.excerpt, ''), left(a.body_text, 300))::text AS excerpt, a.updated_at,
       c.id AS category_id, c.name AS category_name, c.slug AS category_slug, c.updated_at AS category_updated_at,
       p.name AS parent_name, p.slug AS parent_slug
FROM kb_articles a
JOIN kb_categories c ON c.id = a.category_id
LEFT JOIN kb_categories p ON p.id = c.parent_id
WHERE a.slug = $1 AND a.status = 'published';

-- name: KbPublicRelatedArticles :many
SELECT a.title, a.slug
FROM kb_articles a
WHERE a.category_id = @category_id AND a.status = 'published' AND a.id <> @id
ORDER BY a.position, a.title, a.id
LIMIT 5;

-- name: KbPopularArticles :many
SELECT a.title, a.slug, COALESCE(NULLIF(a.excerpt, ''), left(a.body_text, 300))::text AS excerpt, c.name AS category_name
FROM kb_articles a JOIN kb_categories c ON c.id = a.category_id
WHERE a.status = 'published'
ORDER BY a.view_count DESC, a.updated_at DESC, a.id
LIMIT @page_size::integer;

-- name: KbSearchPublished :many
-- headline_options carries private-use marker characters as StartSel/StopSel; the caller
-- escapes the snippet and only then turns the markers into <mark>.
SELECT a.title, a.slug, c.name AS category_name,
       ts_rank_cd(a.fts, q.query, 32)::float8 AS rank,
       ts_headline('dutch', CASE WHEN a.body_text = '' THEN a.title ELSE a.body_text END, q.query, @headline_options::text) AS snippet
FROM kb_articles a
JOIN kb_categories c ON c.id = a.category_id
CROSS JOIN LATERAL (SELECT websearch_to_tsquery('dutch', @text::text) || websearch_to_tsquery('echoo_simple', @text::text) AS query) q
WHERE a.status = 'published' AND a.fts @@ q.query
ORDER BY rank DESC, a.title, a.id
LIMIT @page_size::integer;

-- name: KbSitemapArticles :many
SELECT slug, updated_at FROM kb_articles WHERE status = 'published' ORDER BY slug;

-- name: KbSitemapCategories :many
SELECT c.slug, c.updated_at FROM kb_categories c
WHERE EXISTS (SELECT 1 FROM kb_articles a WHERE a.status = 'published'
              AND (a.category_id = c.id OR a.category_id IN (SELECT ch.id FROM kb_categories ch WHERE ch.parent_id = c.id)))
ORDER BY c.slug;

-- name: KbCountView :exec
UPDATE kb_articles SET view_count = view_count + 1 WHERE slug = $1 AND status = 'published';

-- name: KbRecordFeedback :execrows
UPDATE kb_articles
SET helpful_yes = helpful_yes + (@helpful::boolean)::integer,
    helpful_no = helpful_no + (NOT @helpful::boolean)::integer
WHERE id = @id AND status = 'published';
