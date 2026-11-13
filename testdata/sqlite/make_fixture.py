#!/usr/bin/env python3
"""Makes dalibot.db, the SQLite fixture of the gallery's tests, with the bot's own store code.

    DALIBOT_STORE=sqlite DALIBOT_DB=/tmp/fixture/dalibot.db \
        python3 -B testdata/sqlite/make_fixture.py /path/to/dalibot
    cp /tmp/fixture/dalibot.db testdata/sqlite/dalibot.db

Only the bot's lib/store.py (and the lib/config.py it reads) is imported. Nothing is sent anywhere, and
-B keeps Python from writing into the bot's directory. The bot stamps each row with the time of
writing, so the clock the store sees is replaced by one that this script sets: the fixture is the same
every time, and "now" for the tests is NOW below (internal/source/sqlite_test.go uses the same number).
"""
import os
import sys

NOW = 1800000000
DAY = 86400
GROUP = -1001234567890   # a group chat
PRIVATE = 424242         # a private chat
OTHER = -1009876543210   # a second group, for the chat allow-list

if os.environ.get('DALIBOT_STORE') != 'sqlite' or not os.environ.get('DALIBOT_DB', '').startswith('/'):
	sys.exit('set DALIBOT_STORE=sqlite and DALIBOT_DB=<absolute path of a new file>')
if os.path.exists(os.environ['DALIBOT_DB']):
	sys.exit('DALIBOT_DB already exists; give the path of a new file')
if len(sys.argv) != 2:
	sys.exit('usage: make_fixture.py /path/to/dalibot')
sys.path.insert(0, sys.argv[1])

from lib import store # noqa: E402

class Clock:
	"""Stands in for the time module inside lib/store.py."""
	now = NOW
	def time(self):
		return self.now

clock = Clock()
store.time = clock

def at(age):
	clock.now = NOW - age

def fid(name):
	return f'AgACAgUAAxkDAAIsql-FIXTURE-{name}_AAnK6MRtYq2lV'

# The oldest write comes first: the store purges old rows once, when it is first used.
# A record the bot would purge at its next start: 91 days old.
at(91 * DAY)
store.save_image(GROUP, 90, 'a forgotten castle', 'dream', None, fid('expired'), 'A forgotten castle on a hill')
# One that is 89 days old is still kept.
at(89 * DAY)
store.save_image(GROUP, 91, 'an old oak tree', 'dream', None, fid('oak'), 'An old oak tree in a meadow')

# An ordinary record with three styles; a button press then shows the plain image again.
at(5000)
store.save_image(GROUP, 101, 'a red fox in snow', 'dream', None, fid('fox0'), 'A red fox standing in deep snow, soft morning light', base='a red fox in snow')
at(4000)
store.save_image(GROUP, 101, 'a red fox in snow', 'dream', 'monet', fid('fox1'), 'A red fox standing in deep snow, in the style of Claude Monet', base='a red fox in snow')
at(3900)
store.save_image(GROUP, 101, 'a red fox in snow', 'dream', 'dali', fid('fox2'), 'A red fox standing in deep snow, in the style of Salvador Dali', base='a red fox in snow')
at(3800)
store.set_current(GROUP, 101, None)

# A single image made with another command.
at(3000)
store.save_image(GROUP, 102, 'lighthouse in a storm', 'flux', None, fid('lighthouse'), 'A lighthouse in a storm, waves breaking')

# A picture from before the history existed, imported today: it keeps the date it was first sent.
at(2000)
store.import_image(GROUP, 'fAQADimport1', 'a paper boat on a wide river', store.STORE_ONLY_COMMAND, fid('boat'), NOW - 40 * DAY)

# A variations album made from a photo: one record for each photo, kind "variation".
at(1500)
store.save_variations(GROUP, 'make it cosy', 'dream',
	[{'message_id': 201, 'file_id': fid('cosy1')}, {'message_id': 202, 'file_id': fid('cosy2')}],
	['watercolour, seen from above', 'oil painting, close up'])
at(1450)
store.save_variations(GROUP, '', 'dream', [{'message_id': 203, 'file_id': fid('plain')}], ['pencil sketch, wide view'])

# An album note: a record with no images.
at(1400)
store.remember_album(GROUP, 77, 'holiday pictures')

# A record that was deleted with #delete: in SQLite the row is gone.
at(1300)
store.save_image(GROUP, 103, 'a mistake', 'dream', None, fid('deleted'), 'A mistake')
at(1200)
if store.forget(GROUP, 103) != 1:
	sys.exit('forget did not remove the record')

# A private chat: a message that could not be edited, so its images were carried over to a new one.
at(1000)
store.save_image(PRIVATE, 301, 'tea party on the moon', 'dream', None, fid('tea0'), 'A tea party on the moon')
at(950)
store.save_image(PRIVATE, 301, 'tea party on the moon', 'dream', 'bw', fid('tea1'), 'A tea party on the moon, black and white')
at(900)
if not store.carry_over(PRIVATE, 301, 302, current='bw'):
	sys.exit('carry_over failed')

# Another group.
at(800)
store.save_image(OTHER, 401, 'a city made of books', 'dream', None, fid('books'), 'A city made of books')

at(0)
print(store.backend().describe())
