CREATE TABLE student_profiles (
  id uuid PRIMARY KEY,
  user_id uuid NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
  status varchar(16) NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL
);
CREATE INDEX student_profiles_status ON student_profiles(status);

CREATE TABLE mentor_profiles (
  id uuid PRIMARY KEY,
  user_id uuid NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
  status varchar(16) NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
  granted_by uuid REFERENCES users(id) ON DELETE SET NULL,
  granted_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL
);
CREATE INDEX mentor_profiles_status ON mentor_profiles(status);
CREATE INDEX mentor_profiles_granted_by ON mentor_profiles(granted_by);

INSERT INTO student_profiles (id, user_id, status, created_at, updated_at)
SELECT gen_random_uuid(), id, CASE WHEN status = 'active' THEN 'active' ELSE 'disabled' END, created_at, updated_at
FROM users
WHERE role IN ('student', 'mentor');

INSERT INTO mentor_profiles (id, user_id, status, granted_by, granted_at, created_at, updated_at)
SELECT gen_random_uuid(), id, CASE WHEN status = 'active' THEN 'active' ELSE 'disabled' END, NULL, created_at, created_at, updated_at
FROM users
WHERE role = 'mentor';

UPDATE users SET role = 'student', updated_at = now()
WHERE role = 'mentor';
