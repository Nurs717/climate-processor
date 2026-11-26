CREATE TABLE readings (
    device_id   TEXT    NOT NULL CHECK (device_id <> ''),
    ts          INTEGER NOT NULL, -- Unix seconds, UTC
    temperature REAL    NOT NULL, -- degrees Celsius
    humidity    REAL    NOT NULL, -- percent
    pressure    REAL    NOT NULL, -- Pascals
    PRIMARY KEY (device_id, ts)
) WITHOUT ROWID;
