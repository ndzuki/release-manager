-- The postgres:16 image creates the POSTGRES_USER and POSTGRES_DB
-- (release_manager) automatically and runs this script as that superuser.
-- Create the second per-authority database the notifier owns (REQ-031/REQ-070).
CREATE DATABASE release_notifier OWNER release_manager;
