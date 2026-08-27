-- Neither effect can be undone from here: the rows are gone and the sequence position
-- was not recorded. This file exists so the fixture exercises classification, not the
-- missing-down cap.
SELECT count(*) FROM orders;
