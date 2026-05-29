-- Soft-delete for users.
--
-- Hard-deleting a user row would cascade-delete every plugin they own, because
-- plugins.owner_id REFERENCES users(id) ON DELETE CASCADE. We don't want to
-- lose published plugins when an account is removed, so "deleting" a user now
-- flips their status to 'deleted' instead: the row survives as a valid plugin
-- owner reference, but the account is hidden from the user directory and can no
-- longer authenticate (login requires status = 'approved').

ALTER TABLE users DROP CONSTRAINT IF EXISTS users_status_check;

ALTER TABLE users
    ADD CONSTRAINT users_status_check
    CHECK (status IN ('approved', 'pending', 'rejected', 'deleted'));
