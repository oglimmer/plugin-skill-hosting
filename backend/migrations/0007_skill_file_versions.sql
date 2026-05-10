-- Skill file version snapshots
--
-- Every skill_versions row is paired with a snapshot of the entire skill_files
-- tree at that moment, so revert restores SKILL.md (description+body) and the
-- supporting files together as one atomic prior state.

CREATE TABLE IF NOT EXISTS skill_file_versions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    skill_version_id UUID NOT NULL REFERENCES skill_versions(id) ON DELETE CASCADE,
    path TEXT NOT NULL,
    content_text TEXT,
    content_blob BYTEA,
    is_binary BOOLEAN NOT NULL DEFAULT false,
    size_bytes INTEGER NOT NULL DEFAULT 0,
    UNIQUE (skill_version_id, path)
);

CREATE INDEX IF NOT EXISTS skill_file_versions_version_idx ON skill_file_versions(skill_version_id);
