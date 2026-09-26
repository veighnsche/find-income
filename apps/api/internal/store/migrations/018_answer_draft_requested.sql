-- C4/R03: an explicit owner choice to leave a required answer blank and
-- have Standard draft it from verified facts during Prepare.
ALTER TABLE answer_values ADD COLUMN draft_requested INTEGER NOT NULL DEFAULT 0;
