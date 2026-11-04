-- migrations/tenant/00007_target_recurrence_schedule.sql
-- Module: Target Recurrence Schedule Specification (BALANCE-101)

-- 1. Add recurrence schedule fields to targets_projection
ALTER TABLE targets_projection ADD COLUMN recurrence_day_of_week INTEGER CHECK(recurrence_day_of_week IS NULL OR (recurrence_day_of_week >= 1 AND recurrence_day_of_week <= 7));
ALTER TABLE targets_projection ADD COLUMN recurrence_day_of_month INTEGER CHECK(recurrence_day_of_month IS NULL OR (recurrence_day_of_month >= 1 AND recurrence_day_of_month <= 31));
ALTER TABLE targets_projection ADD COLUMN recurrence_month_of_year INTEGER CHECK(recurrence_month_of_year IS NULL OR (recurrence_month_of_year >= 1 AND recurrence_month_of_year <= 12));

-- 2. Backfill existing targets_projection rows based on due_date and recurrence_cadence
-- For WEEKLY: derive ISO day of week 1..7 (strftime('%w') is 0=Sun..6=Sat; ISO is 1=Mon..7=Sun)
UPDATE targets_projection
SET recurrence_day_of_week = CASE
    WHEN due_date IS NOT NULL AND due_date > 0 THEN
        CASE cast(strftime('%w', due_date / 1000000, 'unixepoch') AS INTEGER)
            WHEN 0 THEN 7
            ELSE cast(strftime('%w', due_date / 1000000, 'unixepoch') AS INTEGER)
        END
    ELSE 1
END
WHERE recurrence_cadence = 'WEEKLY';

-- For MONTHLY: derive day of month 1..31
UPDATE targets_projection
SET recurrence_day_of_month = CASE
    WHEN due_date IS NOT NULL AND due_date > 0 THEN
        cast(strftime('%d', due_date / 1000000, 'unixepoch') AS INTEGER)
    ELSE 1
END
WHERE recurrence_cadence = 'MONTHLY';

-- For ANNUAL: derive day of month and month of year
UPDATE targets_projection
SET recurrence_day_of_month = CASE
    WHEN due_date IS NOT NULL AND due_date > 0 THEN
        cast(strftime('%d', due_date / 1000000, 'unixepoch') AS INTEGER)
    ELSE 1
END,
recurrence_month_of_year = CASE
    WHEN due_date IS NOT NULL AND due_date > 0 THEN
        cast(strftime('%m', due_date / 1000000, 'unixepoch') AS INTEGER)
    ELSE 1
END
WHERE recurrence_cadence = 'ANNUAL';

-- For CUSTOM_MONTHS and CUSTOM_YEARS: derive day and month from due_date
UPDATE targets_projection
SET recurrence_day_of_month = CASE
    WHEN due_date IS NOT NULL AND due_date > 0 THEN
        cast(strftime('%d', due_date / 1000000, 'unixepoch') AS INTEGER)
    ELSE 1
END,
recurrence_month_of_year = CASE
    WHEN due_date IS NOT NULL AND due_date > 0 THEN
        cast(strftime('%m', due_date / 1000000, 'unixepoch') AS INTEGER)
    ELSE 1
END
WHERE recurrence_cadence IN ('CUSTOM_MONTHS', 'CUSTOM_YEARS');
