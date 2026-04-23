-- Simplified schema for LLM-based classification

-- Feed sources
CREATE TABLE IF NOT EXISTS feeds (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    url TEXT NOT NULL UNIQUE,
    title TEXT DEFAULT '',
    description TEXT DEFAULT '',
    last_fetched DATETIME,
    next_fetch DATETIME,
    fetch_interval INTEGER DEFAULT 1800, -- 30 minutes
    icon_url TEXT DEFAULT '',
    error_count INTEGER DEFAULT 0,
    last_error TEXT DEFAULT '',
    enabled BOOLEAN DEFAULT 1,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Articles with LLM classification
CREATE TABLE IF NOT EXISTS items (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    feed_id INTEGER NOT NULL,
    guid TEXT NOT NULL,
    title TEXT NOT NULL,
    link TEXT NOT NULL,
    description TEXT DEFAULT '',
    content TEXT DEFAULT '',        -- Original RSS content
    author TEXT DEFAULT '',
    published DATETIME,
    
    -- Extracted content
    extracted_content TEXT DEFAULT '',   -- Full article text
    extracted_rich_content TEXT DEFAULT '',  -- HTML formatted content
    extracted_at DATETIME,
    extraction_error TEXT DEFAULT '',
    
    -- LLM classification results
    relevance_score REAL DEFAULT 0,     -- 0-10 score from LLM
    explanation TEXT DEFAULT '',         -- Why this score
    topics JSON DEFAULT '[]',             -- Detected topics/tags
    summary TEXT DEFAULT '',             -- Article summary
    classified_at DATETIME,
    
    -- User feedback
    user_feedback TEXT DEFAULT '',      -- 'like', 'dislike', 'done', 'spam', empty
    feedback_at DATETIME,

    -- Processing state: when the user dismissed the article from the main board
    -- (any of like / dislike / done sets this). Null = still in inbox.
    processed_at DATETIME,

    -- Metadata
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    
    UNIQUE(feed_id, guid),
    FOREIGN KEY (feed_id) REFERENCES feeds(id) ON DELETE CASCADE
);

-- User preferences and settings
CREATE TABLE IF NOT EXISTS settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Indexes for performance
CREATE INDEX IF NOT EXISTS idx_items_published ON items(published DESC);
CREATE INDEX IF NOT EXISTS idx_items_score ON items(relevance_score DESC);
CREATE INDEX IF NOT EXISTS idx_items_feedback ON items(user_feedback, feedback_at DESC);
CREATE INDEX IF NOT EXISTS idx_feeds_next ON feeds(next_fetch);

-- Additional performance indexes
CREATE INDEX IF NOT EXISTS idx_items_feed_published ON items(feed_id, published DESC);
CREATE INDEX IF NOT EXISTS idx_items_classification ON items(classified_at, relevance_score DESC);
CREATE INDEX IF NOT EXISTS idx_items_extraction ON items(extracted_at);
CREATE INDEX IF NOT EXISTS idx_items_score_feedback ON items(relevance_score DESC) WHERE user_feedback = '';
CREATE INDEX IF NOT EXISTS idx_items_processed ON items(processed_at);
CREATE INDEX IF NOT EXISTS idx_items_inbox_score ON items(relevance_score DESC) WHERE processed_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_feeds_enabled_next ON feeds(enabled, next_fetch) WHERE enabled = 1;

-- Topic-related indexes for JSON column
CREATE INDEX IF NOT EXISTS idx_items_topics_json ON items(json_extract(topics, '$'));
CREATE INDEX IF NOT EXISTS idx_items_score_classified ON items(relevance_score DESC, classified_at) WHERE classified_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_items_classified_score ON items(classified_at, relevance_score DESC) WHERE classified_at IS NOT NULL;

-- Full-text search support
CREATE VIRTUAL TABLE IF NOT EXISTS items_fts USING fts5(
    title,
    description,
    content,
    extracted_content,
    summary,
    content=items,
    content_rowid=id,
    tokenize='porter unicode61'
);

-- Triggers to keep FTS index in sync
CREATE TRIGGER IF NOT EXISTS items_fts_insert AFTER INSERT ON items BEGIN
    INSERT INTO items_fts(rowid, title, description, content, extracted_content, summary)
    VALUES (new.id, 
            COALESCE(new.title, ''), 
            COALESCE(new.description, ''), 
            COALESCE(new.content, ''), 
            COALESCE(new.extracted_content, ''), 
            COALESCE(new.summary, ''));
END;

CREATE TRIGGER IF NOT EXISTS items_fts_delete AFTER DELETE ON items BEGIN
    DELETE FROM items_fts WHERE rowid = old.id;
END;

CREATE TRIGGER IF NOT EXISTS items_fts_update AFTER UPDATE ON items BEGIN
    INSERT OR REPLACE INTO items_fts(rowid, title, description, content, extracted_content, summary)
    VALUES (new.id, 
            COALESCE(new.title, ''), 
            COALESCE(new.description, ''), 
            COALESCE(new.content, ''), 
            COALESCE(new.extracted_content, ''), 
            COALESCE(new.summary, ''));
END;

-- Update timestamp trigger
CREATE TRIGGER IF NOT EXISTS items_updated_at AFTER UPDATE ON items BEGIN
    UPDATE items SET updated_at = CURRENT_TIMESTAMP WHERE id = new.id;
END;

CREATE TRIGGER IF NOT EXISTS settings_updated_at AFTER UPDATE ON settings BEGIN
    UPDATE settings SET updated_at = CURRENT_TIMESTAMP WHERE key = new.key;
END;

-- Beat aggregation: embedding vectors for classified items
CREATE TABLE IF NOT EXISTS item_embeddings (
    item_id    INTEGER PRIMARY KEY REFERENCES items(id) ON DELETE CASCADE,
    model      TEXT    NOT NULL,
    vector     BLOB    NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Beat aggregation: one beat groups several items reporting the same event
CREATE TABLE IF NOT EXISTS beats (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    canonical_title   TEXT,
    canonical_summary TEXT,
    first_seen_at     DATETIME NOT NULL,
    last_viewed_at    DATETIME,
    feedback          TEXT     DEFAULT '',
    feedback_at       DATETIME,
    created_at        DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at        DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Beat aggregation: membership of items in beats; each item belongs to at most one beat
CREATE TABLE IF NOT EXISTS beat_members (
    beat_id  INTEGER NOT NULL REFERENCES beats(id) ON DELETE CASCADE,
    item_id  INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    added_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (beat_id, item_id)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_beat_members_item ON beat_members(item_id);
CREATE INDEX IF NOT EXISTS idx_beats_pending_merge ON beats(id) WHERE canonical_summary IS NULL;

-- Beat FTS: full-text search over canonical title and summary
CREATE VIRTUAL TABLE IF NOT EXISTS beats_fts USING fts5(
    canonical_title,
    canonical_summary,
    content=beats,
    content_rowid=id,
    tokenize='porter unicode61'
);

CREATE TRIGGER IF NOT EXISTS beats_fts_insert AFTER INSERT ON beats BEGIN
    INSERT INTO beats_fts(rowid, canonical_title, canonical_summary)
    VALUES (new.id, COALESCE(new.canonical_title, ''), COALESCE(new.canonical_summary, ''));
END;

CREATE TRIGGER IF NOT EXISTS beats_fts_delete AFTER DELETE ON beats BEGIN
    DELETE FROM beats_fts WHERE rowid = old.id;
END;

CREATE TRIGGER IF NOT EXISTS beats_fts_update AFTER UPDATE ON beats BEGIN
    INSERT OR REPLACE INTO beats_fts(rowid, canonical_title, canonical_summary)
    VALUES (new.id, COALESCE(new.canonical_title, ''), COALESCE(new.canonical_summary, ''));
END;
