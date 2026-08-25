# Baraholler

Baraholler is a small Go utility for personal use: it watches public Telegram marketplace channels for items of interest.

It signs in through MTProto as a regular Telegram user, checks new and edited posts for literal terms, and sends matching posts to Telegram, macOS notifications, or both. It is deliberately a local utility, not a hosted service or a general-purpose monitoring platform.

## What it does

- Matches Unicode text and media captions case-insensitively.
- Never joins sources or marks messages as read on your behalf.
- Recovers updates missed while it was offline.
- Deduplicates notifications across edits and restarts.
- Retries failed destinations independently and persists pending retries.
- Falls back to an excerpt and direct link when Telegram prevents forwarding protected content.

## Configuration

A rule connects one or more sources and search terms to a notification destination:

```yaml
rules:
  - name: "laptop"
    sources:
      - "https://t.me/local_marketplace"
    terms:
      - "macbook"
      - "mac book"
    notifications:
      telegram:
        enabled: true
        recipient: "@recipient_username"
```

The annotated [config.example.yml](config.example.yml) shows all available fields.

## Run locally

Requirements: Go 1.25+, GNU Make, an existing Telegram account, and API credentials from [my.telegram.org](https://my.telegram.org).

Baraholler uses MTProto rather than the Bot API because bots cannot observe marketplace channels on behalf of a user. The integration is built on [gotd/td](https://github.com/gotd/td), a pure Go MTProto client, together with its official companion packages for persistent update state, peer caching, `FLOOD_WAIT` handling, and request rate limiting.

```sh
git clone https://github.com/GeorgeGorbanev/baraholler.git
cd baraholler
make prepare
```

Edit the generated `config.yml`. The account must already be a member of every configured source.

```sh
make check-config
make run
```

The first run asks for a Telegram login code and, when enabled, a 2FA password. The password is read from an interactive terminal with echo disabled and is never saved. By default, session and state files stay in the local `.data` directory.

## Behavior worth knowing

- The first run starts from the current Telegram state; it does not backfill old posts.
- Later update gaps are recovered through Telegram, with a bounded history fallback for configured sources when necessary.
- Delivery is at least once. A crash between an external send and its local delivery marker can rarely produce a duplicate.

## Limitations

- Sources must be public username-based channels or supergroups, and the account must already be a member.
- Matching is literal; regular expressions and fuzzy matching are not supported.
- Native desktop notifications are available only on macOS. Telegram notifications work on any Go-supported desktop or server platform.

## Development

```sh
make fmt
make check
make build
```
