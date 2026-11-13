# Hiding images (admin)

An admin can take an image out of the gallery and put it back later. This
page covers how to turn it on, how it is used, where the gallery keeps what
it needs, and what an admin can and cannot do.

## Hiding is not deleting

The gallery never writes to the bot's data: not to the DynamoDB table, not
to the bot's SQLite file. So "Hide" does not delete anything. The gallery
keeps its own list of hidden images, on its own disk, and leaves every image
on that list out of what it shows. The bot, its records and the pictures in
Telegram are unchanged.

To remove a picture for good, use the bot's `#delete`. That removes the
record itself, and the gallery drops it at its next refresh, whatever the
gallery's list says.

## Turning it on

Hiding is available when `GALLERY_ADMIN_TOKEN` is set (see
[Who can view and who can administer](../README.md#who-can-view-and-who-can-administer)).
Without an admin token there are no admin controls and no admin routes.
Images hidden while there was an admin token stay hidden after it is
removed.

Give the gallery a directory for its state, `GALLERY_STATE_DIR`:

| Install | Where the list is kept |
|---|---|
| `deploy/compose.yaml` | the named volume `state`, at `/var/lib/dalibot-gallery` |
| `docker run` | the image sets `GALLERY_STATE_DIR=/var/lib/dalibot-gallery`; mount a named volume there (`-v dalibot-gallery-state:/var/lib/dalibot-gallery`), or Docker makes an anonymous one that is lost when the container is removed |
| `deploy/dalibot-gallery.service` | `/var/lib/dalibot-gallery` (`StateDirectory=`; systemd passes it as `$STATE_DIRECTORY`). With `DynamicUser=yes` the directory is really `/var/lib/private/dalibot-gallery` |
| anything else | the directory `GALLERY_STATE_DIR` names; it is created (mode 0700) if missing |

If neither `GALLERY_STATE_DIR` nor `$STATE_DIRECTORY` is set, the list is
kept in the cache directory, and the gallery says so in a warning at
start-up (when an admin token is set or something is hidden). That works,
but the cache is meant to be deletable: deleting it would show every hidden
image again.

The list is state, not cache. Include the state directory in backups.

## Using it

Sign in at `/login` with the admin token. In a public gallery the small
"Admin" link at the foot of the page leads there.

**In the viewer.** Under the large image, at the end of the line with the
command and style, there is a "Hide" button. It asks first: "Hide this image
from the gallery?" with "Cancel" and "Hide image". Only "Hide image" hides,
and only:

- for the image the question was asked about (moving to another image, with
  the arrows, the strip or a swipe, withdraws the question);
- after a moment: a double click on "Hide", or a second tap that lands where
  "Hide" was, does nothing;
- never from the image itself: a tap or click on the image, a drag or a
  swipe does not hide.

The question opens with the focus on "Cancel", so Enter held down on "Hide"
does not go through either. With the keyboard: Tab to "Hide", Enter, Tab to
"Hide image", Enter.

After hiding, the tile leaves the grid, the viewer moves to the next image
(the one before, if it was the last), and "Image hidden. Undo" is offered
for 30 seconds: in the viewer while it is open, at the foot of the window
when it is closed. Undo puts the image back where it was.

**The page of hidden images.** "Hidden images", at the foot of the gallery,
opens `/admin/hidden`: every hidden image that is still in the bot's
records, most recently hidden first, each with a "Restore" button. Images
that are on the list but no longer in the bot's records (expired, deleted
with `#delete`, or outside `GALLERY_CHATS`) cannot be shown; the page gives
their number. They stay on the list, so if such an image comes back (for
example after `GALLERY_CHATS` changes), it is still hidden.

A change is in force when the gallery answers it: from then on the image is
missing from the listing for every visitor, and its image URLs answer `404`
exactly as for an image that never existed. Pages that were already open
elsewhere keep the tile until they are reloaded or load it again; its image
can no longer be fetched.

## How it works

The list is one file, `hidden.json`, in the state directory:

```json
{"version":1,"hidden":[{"key":"RQ2AvWdtw2pN5Bhv0Y3CdX","at":1799999000}]}
```

Each entry is an image key (the 22 characters in the image's URL) and the
time it was hidden. Nothing else about the image is stored: no chat id, no
caption, no file id.

- **Writes are atomic.** Every change is written to a temporary file in the
  same directory, synced, and renamed over `hidden.json`, so a crash or a
  power cut leaves either the old list or the new one. A change that cannot
  be written is not made, and the admin is told.
- **A missing file** is an empty list.
- **A file that cannot be understood** (cut off, not JSON, an unknown
  version) is never overwritten. At start-up it is renamed to
  `hidden.json.corrupt-<time>`, the error is logged, and the gallery starts
  with nothing hidden. If it cannot even be renamed, the gallery starts with
  nothing hidden and refuses to hide or restore until the file is repaired
  or removed. Entries whose key is not an image key are skipped with a
  warning.
- **A file that exists but cannot be read** (permissions) stops the
  gallery at start-up, rather than show everything that was hidden.
- **The file is read at start-up only.** To edit it by hand, or to put
  back a copy kept as `hidden.json.corrupt-…` after repairing it, stop the
  gallery, change the file, and start it again. Changes made while it runs
  are overwritten by the next hide or restore.
- At most 100,000 images can be hidden.

Hiding applies the same rule to both sources, DynamoDB and SQLite, after
the usual listing rules: a hidden image does not come back through an older
record that has the same picture. In a record with several styles, only the
hidden style goes; the record's other tiles stay, and the hidden one is no
longer among their styles.

The cache keeps a hidden image's original and thumbnails while its record
lasts, so restoring it needs no second download from Telegram (which may no
longer be possible). The files go when the record leaves the bot's records,
as for any image.

## The routes

They exist only with an admin token, and only for an admin. To everyone
else, signed in to view or not, they answer exactly as a path that does not
exist, whether or not the key is real: `404`, or, for a visitor who is not
signed in to a gallery that needs the token, the usual redirect to `/login`
(`401` for `/api/`). A `POST` from anyone but an admin is always `404`.

| Route | |
|---|---|
| `GET /admin/hidden` | The page of hidden images. |
| `GET /api/admin/hidden` | The same list as JSON: `{"hidden":[{"key","style","label","caption","prompt","command","updated","hidden_at"}], "gone": <n>, "v": "<tag>"}`. No record id. |
| `POST /api/admin/hide/<key>` | Hide an image of the bot's records. `200 {"key":…,"hidden":true}`; hiding one that is hidden already also succeeds. |
| `POST /api/admin/restore/<key>` | Put a hidden image back. `200 {"key":…,"hidden":false}`. |

An unknown key is `404`. A `POST` from another site's page is `403` and
changes nothing (`http.CrossOriginProtection`, as for `/login`). The admin's
session cookie, or HTTP Basic with the admin token as the password, is
needed, so a script can use them:

```sh
curl -u admin:"$GALLERY_ADMIN_TOKEN" -X POST https://gallery.example.org/api/admin/hide/RQ2AvWdtw2pN5Bhv0Y3CdX
```

An admin is still served a hidden image's thumbnail and original (the page
of hidden images shows them), but with `Cache-Control: no-store` and no
`ETag`, so that no cache keeps a copy under the image's public URL.

## What an admin can and cannot do

An admin can:

- view, as a viewer can (in a public gallery, an admin also receives the
  record ids in the JSON listing, which contain the Telegram chat id);
- hide any image of the bot's records that the gallery shows, and restore
  any image on the list;
- see the hidden images.

An admin cannot:

- change or delete anything of the bot's: its records, its database, its
  pictures in Telegram. Hiding is the gallery's own list only;
- make the gallery show anything that is not in the bot's records, or reach
  any file but those images;
- see another visitor's session, a token, or a Telegram file id.

Hidden is not secret. A hidden image is still in the bot's records and in
the chat it was made in, and anyone who saved it, or whose browser or a
shared cache stored it before it was hidden, still has it. In a public
gallery, images are served with `Cache-Control: public`, so a proxy cache
in front of the gallery may go on serving a copy it already holds until
that copy expires or is purged.

The admin's script and stylesheet (`/admin.js`, `/admin.css`) are sent to
admins only, and only an admin's page refers to them: an ordinary visitor's
page has no admin controls and downloads nothing extra. Every hide and
restore is logged with the image key and the client address.
