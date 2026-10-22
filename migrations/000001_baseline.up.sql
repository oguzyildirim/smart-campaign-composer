-- Baseline migration. It establishes migration tracking
-- (schema_migrations) without creating application tables; the schema
-- for users, consents, and events arrives with Milestone 2.
COMMENT ON SCHEMA public IS 'Smart Campaign Composer application schema';
