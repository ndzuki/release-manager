-- Customer optimistic-lock version for AC-051-02. The version column is
-- backfilled to 1 for existing rows and starts at 1 for new customers.
ALTER TABLE customers ADD COLUMN version BIGINT NOT NULL DEFAULT 1;
