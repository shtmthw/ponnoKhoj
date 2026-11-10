-- name: CreateUser :one
INSERT INTO users (email, password_hash, display_name, role, is_demo)
VALUES (@email, @password_hash, @display_name, @role, @is_demo)
ON CONFLICT (email) DO NOTHING
RETURNING id;

-- name: GetUserByEmail :one
SELECT id, email, password_hash, display_name, role, failed_logins, locked_until
FROM users WHERE email = @email;

-- name: GetUserByID :one
SELECT id, email, display_name, role FROM users WHERE id = @id;

-- name: RecordLoginFailure :exec
UPDATE users SET failed_logins = failed_logins + 1, locked_until = sqlc.narg(locked_until)
WHERE id = @id;

-- name: ResetLoginFailures :exec
UPDATE users SET failed_logins = 0, locked_until = NULL WHERE id = @id;

-- name: SetUserPassword :exec
UPDATE users SET password_hash = @password_hash WHERE id = @id;

-- name: SetUserRole :exec
UPDATE users SET role = @role WHERE id = @id;

-- name: CreateSession :exec
INSERT INTO sessions (user_id, token_hash, expires_at) VALUES (@user_id, @token_hash, @expires_at);

-- name: GetSessionUser :one
SELECT u.id, u.email, u.display_name, u.role, s.expires_at
FROM sessions s
JOIN users u ON u.id = s.user_id
WHERE s.token_hash = @token_hash AND s.expires_at > now();

-- name: DeleteSession :exec
DELETE FROM sessions WHERE token_hash = @token_hash;

-- name: DeleteExpiredSessions :execrows
DELETE FROM sessions WHERE expires_at <= now();
