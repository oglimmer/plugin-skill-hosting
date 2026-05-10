-- Skill files
--
-- Skills can include supporting files alongside the SKILL.md body:
--   scripts/    — executable code Claude can run
--   references/ — supporting docs Claude reads on demand
--   assets/     — templates, fonts, icons used in output
--
-- Each row stores either UTF-8 text in content_text or arbitrary bytes in
-- content_blob, picked at upload time based on whether the payload decodes
-- as valid UTF-8.

CREATE TABLE IF NOT EXISTS skill_files (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    skill_id UUID NOT NULL REFERENCES skills(id) ON DELETE CASCADE,
    path TEXT NOT NULL,
    content_text TEXT,
    content_blob BYTEA,
    is_binary BOOLEAN NOT NULL DEFAULT false,
    size_bytes INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (skill_id, path)
);

CREATE INDEX IF NOT EXISTS skill_files_skill_idx ON skill_files(skill_id);
