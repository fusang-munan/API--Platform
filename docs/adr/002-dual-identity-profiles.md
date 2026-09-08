# ADR 002: Dual identity via student_profiles and mentor_profiles

## Status

Accepted

## Context

- One email/password account may act as both student and mentor.
- Only students self-register; teachers grant mentor identity.
- Student and mentor capabilities differ (Key applications vs project review, quota management).
- Legacy `users.role` is a single immutable enum; mentor self-registration exists today.

## Decision

### Account vs identity

| Layer | Table | Purpose |
|-------|-------|---------|
| Account | `users` | email, password, name, status |
| Student identity | `student_profiles` | 1:0..1 per user; created on registration |
| Mentor identity | `mentor_profiles` | 1:0..1 per user; created when teacher grants |
| Teacher | `users.role = 'teacher'` | invite-only; no profile table in v1 |

### users.role rules (transitional column)

- **Teacher accounts:** `users.role = 'teacher'` (unchanged).
- **Everyone else:** `users.role = 'student'` after migration and for all new registrations.
- Authorization for student/mentor APIs uses **profile tables + JWT session identity**, not `users.role`.
- `users.role = 'mentor'` is removed by migration (normalized to `student`).

This avoids contradicting "teacher keeps users.role" with "deprecate users.role": the column stays, but only teacher login reads it directly.

### Login flow

1. `POST /auth/login` — verify password; return `{ user, identities: ['student'|'mentor'|'teacher'] }` **without JWT**.
2. If `identities.length === 1`, client may call session immediately.
3. `POST /auth/session` — body `{ identity }`; issue access JWT + refresh cookie.
4. JWT claim `role` = selected **session identity** (keeps existing middleware shape).
5. Redis refresh `Session` stores `identity` for token rotation.

Teacher: `identities = ['teacher']` only (no profile rows).

### Registration

- Only `POST /auth/register/student`.
- Creates `users` (`role=student`) + `student_profiles` (`active`).
- Remove mentor registration routes and `mentor_registration` verification purpose.

### Grant mentor

- Teacher `POST /teacher/students/:id/grant-mentor` on users with active `student_profiles`.
- Inserts or reactivates `mentor_profiles` with `granted_by`, `granted_at`.
- Does not change `users.role`.

### Legacy data (0010 migration)

| Old `users.role` | Action |
|------------------|--------|
| `student` | `student_profiles` only |
| `mentor` | `student_profiles` + `mentor_profiles`; `users.role → student`; `granted_by` NULL |
| `teacher` | no profile rows |

### Business FKs (v1)

Keep `api_keys.owner_user_id`, `mentor_project_applications.mentor_id`, etc. on `users.id`. Gate APIs by active profile + session identity.

## Consequences

- Dual-identity users choose student or mentor at login; same password.
- Refresh must carry identity; revoking mentor profile blocks mentor login but not student.
- Future: optional `teacher_profiles`, profile-scoped FKs, in-session identity switch.
