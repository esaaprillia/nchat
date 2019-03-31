nchat - ncurses chat
====================

| **Linux + Mac** |
|-----------------|
| [![Build status](https://travis-ci.org/d99kris/nchat.svg?branch=master)](https://travis-ci.org/d99kris/nchat) |

nchat is a console-based chat client for Linux and macOS with support for
Telegram.

![screenshot](/doc/screenshot.png) 

Usage
=====
Usage:

    nchat [OPTION]

Command-line Options:

    -e, --verbose     enable verbose logging
    -h, --help        display this help and exit
    -s, --setup       set up chat protocol account
    -v, --version     output version information and exit

Interactive Commands:

    Tab               next chat
    Sh-Tab            previous chat
    PageDn            next page
    PageUp            previous page
    Ctrl-e            enable/disable emoji
    Ctrl-s            send message
    Ctrl-u            next unread chat
    Ctrl-x            exit

Emojis can be entered on the format `:smiley:`.
Refer to [emojicpp](/tree/master/ext/emojicpp/README.md) for a full list of
supported emojis. One can also toggle display of graphical emojis in the chat
history with `Ctrl-e` to easily see their textual counterpart.

Supported Platforms
===================
nchat is developed and tested on Linux and macOS. Current version has been
tested on:

- macOS 10.14 Mojave
- Ubuntu 18.04 LTS

Build / Install
===============
Nchat consists of a large code-base (mainly the Telegram client library), so be
prepared for a relatively long first build time. Subsequent builds will be
faster with ccache installed.

Linux / Ubuntu
--------------

**Dependencies**

    sudo apt install ccache cmake gperf libreadline-dev libssl-dev libncurses-dev ncurses-doc help2man

**Source**

    git clone https://github.com/d99kris/nchat && cd nchat

**Build**

    mkdir -p build && cd build && cmake .. && make -s

**Install**

    sudo make -s install

macOS
-----

**Dependencies**

    brew install gperf cmake openssl ncurses ccache readline help2man

**Source**

    git clone https://github.com/d99kris/nchat && cd nchat

**Build**

    mkdir -p build && cd build && cmake .. && make -s

**Install**

    sudo make -s install

Getting Started
===============
In order to configure / setup an account one needs to run nchat in setup mode:

    nchat --setup

The setup mode prompts for phone number, which shall be entered with country
code. Example:

    $ nchat --setup
    Protocols:
    0. telegram
    1. Exit setup
    Select protocol (0): 0
    Enter phone number: +6511111111
    Enter authentication code: xxxxx
    Saving to /home/d99kris/.nchat/main.conf

If you are not sure what phone number to enter, open Telegram on your phone
and press the menu button and use the number displayed there (omitting spaces,
so for the below screenshot the number to enter is +6511111111).

![screenshotPhone](/doc/screenshot_phone.png) 

Once the setup process is completed nchat will exit, and can now be restarted
in normal mode:

    nchat

Troubleshooting
===============
If any issues are observed, try running nchat with verbose logging

    nchat --verbose

and provide a copy of ~/.nchat/td.log when reporting the issue. The
preferred way of reporting issues and asking questions is by opening 
[a Github issue](https://github.com/d99kris/nchat/issues/new). 

Security
========

Telegram
--------
Telegram user data is locally stored in `~/.nchat/tdlib` encrypted with a key
randomly generated by nchat (not cryptographically secure PRNG). The key is
stored as plain text in `~/.nchat/telegram.conf`. Default file permissions
only allow user access, but anyone who can gain access to a user's private
files can also access the user's personal Telegram data. To protect against
the most simple attack vectors it may be suitable to use disk encryption and
to ensure `~/.nchat` is not backed up unencrypted.

Configuration
=============
The following configuration files (listed with current default values) can be
used to configure nchat.

~/.nchat/main.conf
------------------
This configuration file determines which protocols should be enabled
(currently Telegram is the only supported). Protocols are automatically
enabled after succesful setup, and thus the protocol
(e.g. `telegram_is_enabled`) only needs to be manually configured if wanting
to disable a protocol.

The `ui` parameter controls which UI plugin/skin to use. There are currently
two supported:
- `uidefault` which can be seen in the main screenshot above
- `uilite` which is a lightweight interface with no contact list

    telegram_is_enabled=0
    ui=uidefault

~/.nchat/telegram.conf
----------------------
This configuration file should not be edited manually by end users.

    local_key=

~/.nchat/uidefault.conf
-----------------------
This configuration file (and uilite.conf for `uilite`) controls the UI aspects,
in particular subwindows size (`input_rows`, `list_width`), shortcut keys and
whether to show emojis graphically.

    input_rows=3
    key_backspace=KEY_BACKSPACE
    key_curs_down=KEY_DOWN
    key_curs_left=KEY_LEFT
    key_curs_right=KEY_RIGHT
    key_curs_up=KEY_UP
    key_delete=KEY_DC
    key_exit=KEY_CTRLX
    key_linebreak=KEY_RETURN
    key_next_chat=KEY_TAB
    key_next_page=KEY_NPAGE
    key_next_unread=KEY_CTRLU
    key_prev_chat=KEY_BTAB
    key_prev_page=KEY_PPAGE
    key_send=KEY_CTRLS
    key_toggle_emoji=KEY_CTRLE
    list_width=14
    show_emoji=1

Technical Details
=================
nchat is implemented in C++. Its source tree includes the source code of the
following third-party libraries:

- [apathy](https://github.com/dlecocq/apathy) - MIT License
- [emojicpp](https://github.com/shalithasuranga/emojicpp) - MIT License
- [TDLib](https://github.com/tdlib/td) - Boost Software License

License
=======
nchat is distributed under the MIT license. See LICENSE file.

Keywords
========
command line, console based, linux, macos, chat client, telegram, ncurses,
terminal.
