-- A template is an email written once and sent many times: a subject and
-- bodies that name variables, filled when an email is asked for. It is
-- what whoever runs the server wrote, not what any one email said: an
-- email made from it is kept, or not, as any other is.
--
-- A template belongs to a sending domain, or to none ('') and then
-- serves every domain. Its name is how a send asks for it.
CREATE TABLE templates (
    id         uuid PRIMARY KEY DEFAULT uuidv7(),
    domain     text NOT NULL DEFAULT '',
    name       text NOT NULL,
    subject    text NOT NULL,
    html       text NOT NULL DEFAULT '',
    body_text  text NOT NULL DEFAULT '',
    -- The variables its subject and bodies name, in the order they first
    -- come: kept beside them, so a list need not read the bodies.
    variables  text[] NOT NULL DEFAULT '{}',
    -- What a variable is filled with when a send gives it no value.
    defaults   jsonb NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (domain, name)
);
