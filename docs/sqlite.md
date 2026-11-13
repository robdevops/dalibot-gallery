# Reading the bot's SQLite database

A self-hosted dalibot keeps its records in a SQLite file instead of DynamoDB
(the bot's `DALIBOT_STORE=sqlite`, which is its default outside AWS Lambda).
The gallery can read that file. It needs no AWS account, key or region then,
and reads none of the AWS settings.

```sh
GALLERY_SOURCE=sqlite
GALLERY_SQLITE_PATH=/opt/dalibot/var/dalibot.db
```

| Variable | Default | Notes |
|---|---|---|
| `GALLERY_SOURCE` | `dynamodb` | `dynamodb` or `sqlite`. |
| `GALLERY_SQLITE_PATH` | (required with `sqlite`) | The bot's database file: its `DALIBOT_DB`, by default `var/dalibot.db` in the bot's directory. A relative path is taken from the gallery's working directory. Setting it without `GALLERY_SOURCE=sqlite` is an error, so that a forgotten switch cannot quietly read DynamoDB. |
| `GALLERY_STORE_DAYS` | `90` | Must equal the bot's `DALIBOT_STORE_DAYS`. Here it only decides when a row counts as expired (see below); dates do not depend on it. |

Everything else is configured as in the [README](../README.md#configuration):
who can view, `TELEGRAM_BOT_TOKEN`, the cache, the proxy. The gallery and
the bot must run on the same machine, with the file on a local disk: SQLite's
locking is not reliable over network file systems.

## What the gallery does to the file

It reads it and nothing else.

- Every refresh opens the file read-only (SQLite's `mode=ro`, plus
  `query_only`), runs one `SELECT` over the bot's `messages` table, and
  closes the file again. Nothing is kept open between refreshes.
- It never creates the file, never writes to it, and never takes a write
  lock. While the `SELECT` runs (a few milliseconds) it holds SQLite's shared
  read lock, which makes a write by the bot wait that long. The bot waits up
  to 10 seconds for a lock, so it does not notice.
- If the bot is in the middle of a write, the gallery retries for up to 5
  seconds, then gives up until the next refresh.
- When a read fails (the file is missing, unreadable or stays locked), the
  gallery keeps showing the previous listing and logs the error, exactly as
  it does when a DynamoDB scan fails. `/healthz` answers 503 once no read has
  succeeded for three refresh intervals, and until the first one succeeds.
  The gallery starts even if the file does not exist yet, and picks it up
  when it appears.

Two cases need the bot, because repairing them would mean writing:

- **The bot was killed in the middle of a write.** SQLite then leaves a
  journal file (`dalibot.db-journal`) that the next writer uses to repair the
  database. A read-only reader may not do that, so every read fails with
  `attempt to write a readonly database` until the bot has opened the
  database again (it does so when it starts, and on its next write).
- **A database in WAL mode is refused.** Reading one means creating and
  writing its `-shm` and `-wal` files, even for a read-only connection. The
  bot never turns WAL on (it uses SQLite's default rollback journal); if
  someone did, `PRAGMA journal_mode=DELETE` turns it off again.

## How rows become tiles

The bot's table is
`messages(chat_id, message_id, data, updated)`: `data` is the record as
JSON, with the same fields as a DynamoDB item, and `updated` is when the bot
last wrote the row.

- The record id is `<chat_id>:<message_id>`.
- A record is dated from `updated`, or from `created` in the JSON for a
  picture that was imported by forwarding it to the bot.
- A row whose `updated` is more than `GALLERY_STORE_DAYS` old is not shown.
  The bot deletes such rows only when it starts, so without this rule a
  long-running bot's gallery would show pictures past their time.
- Every other rule is the one used for DynamoDB: rows without images (album
  notes, for example) are skipped, an image that appears in two records is
  shown once, in the newer one, the order is newest first, and
  `GALLERY_CHATS` limits the chats.
- A row that is not valid JSON in the bot's shape is skipped and counted in a
  warning in the log; the rest are shown.

Two differences from DynamoDB follow from what the bot stores:

- Choosing an earlier style with a button in Telegram rewrites the row, so
  the record moves to the top of the gallery. With DynamoDB only a newly
  drawn image does that.
- `#delete` in Telegram removes the row itself, so the pictures leave the
  gallery at the next refresh.

## Permissions

The bot creates its database readable by its own user only (mode `0600`).
The gallery should not run as the bot's user, so give a group read access
once. The bot does not change the mode of an existing file, and SQLite
gives its journal file the same mode as the database.

```sh
# as root; "dalibot" is the bot's user and group
chgrp dalibot /opt/dalibot/var /opt/dalibot/var/dalibot.db
chmod g+rx /opt/dalibot/var
chmod g+r  /opt/dalibot/var/dalibot.db
```

The gallery's user must also be able to pass through every directory above
the file.

## Container

Mount the directory that holds the database, read-only, and add the bot's
group to the container. `deploy/compose.sqlite.yaml` is a Compose override
that does both; edit the path and the group id in it, then:

```sh
docker compose -f deploy/compose.yaml -f deploy/compose.sqlite.yaml up -d --build
```

Without Compose, add these to the `docker run` command of the README (the
number is the id of the bot's group, from `getent group dalibot`):

```sh
  -v /opt/dalibot/var:/bot:ro --group-add 1001 \
  -e GALLERY_SOURCE=sqlite -e GALLERY_SQLITE_PATH=/bot/dalibot.db \
```

Mount the directory, not the file alone: SQLite's journal file lives next
to the database and the reader has to see it, and a mount of a single file
keeps showing the old file if the database is ever replaced. The read-only
mount is a second guarantee that the gallery cannot change the bot's data.
`.env` needs no AWS lines.

## systemd

`deploy/dalibot-gallery.service` runs the gallery as a throw-away user with
`ProtectHome=yes`. For SQLite, add the bot's group and, if the bot lives
under `/home`, make that path visible read-only. The unit has both lines,
commented out:

```ini
SupplementaryGroups=dalibot
BindReadOnlyPaths=/home/dalibot/dalibot/var:/run/dalibot-db
```

With the second line, `GALLERY_SQLITE_PATH=/run/dalibot-db/dalibot.db`. A
bot outside `/home` (for example in `/opt/dalibot`) needs only the first.
These unit lines have not been run under a real systemd.

## Trying it

`scripts/dev-stack.sh -source sqlite` runs the gallery against a SQLite
database made for the occasion, with the same sample records as the
DynamoDB dev stack and no AWS stand-in. See [Development](development.md).
