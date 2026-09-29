-- name: ListTeams :many
SELECT teams.id, teams.name, teams.created_at, count(team_members.user_id)::integer AS member_count
FROM teams
LEFT JOIN team_members ON team_members.team_id = teams.id
GROUP BY teams.id
ORDER BY lower(teams.name);

-- name: GetTeam :one
SELECT * FROM teams WHERE id = $1;

-- name: GetTeamForUpdate :one
SELECT * FROM teams WHERE id = $1 FOR UPDATE;

-- name: CreateTeam :one
INSERT INTO teams (name) VALUES ($1) RETURNING *;

-- name: RenameTeam :one
UPDATE teams SET name = $2 WHERE id = $1 RETURNING *;

-- name: DeleteTeam :execrows
DELETE FROM teams WHERE id = $1;

-- name: ListTeamMemberIDs :many
SELECT user_id FROM team_members WHERE team_id = $1;

-- name: DeleteTeamMembers :exec
DELETE FROM team_members WHERE team_id = $1;

-- name: AddTeamMembers :exec
INSERT INTO team_members (team_id, user_id)
SELECT $1, unnest(sqlc.arg(user_ids)::uuid[]);

-- name: ListUserTeamIDs :many
SELECT team_id FROM team_members WHERE user_id = $1;
