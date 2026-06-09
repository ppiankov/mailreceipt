# Postfix Pipe Transport Install

This guide wires a local Postfix address such as `receipt@example.com` to
`mailreceipt filter`. All domains and email addresses below are documentation
placeholders. Replace them with your own values on the server.

The pipe transport must receive the MTA-authenticated envelope sender from
Postfix. Do not authorize requests from the message `From:` header.

## Install The Binary

Build or install `mailreceipt` on the Postfix host:

```sh
install -m 0755 mailreceipt /usr/local/bin/mailreceipt
/usr/local/bin/mailreceipt --version
```

The wrapper below assumes `/usr/local/bin/mailreceipt`. If you install somewhere
else, set `MAILRECEIPT_BIN` in the wrapper environment.

## Create The Service Account

Create a locked service account. `mailreceipt` here is a service user, not a
person:

```sh
adduser --system --group --no-create-home --disabled-login mailreceipt
```

If your system uses `useradd`:

```sh
useradd --system --user-group --home-dir /nonexistent --shell /usr/sbin/nologin mailreceipt
```

## Configure mailreceipt

Create the config directory:

```sh
install -d -m 0750 /etc/mailreceipt
chgrp mailreceipt /etc/mailreceipt
```

Create `/etc/mailreceipt/.mailreceipt.yml`:

```yaml
log: /var/log/mail.log

receipt_filter:
  domains: [example.com]
  reply_from: receipt@example.com
  teams:
    support:
      members:
        - sender@example.com
        - member@example.com
```

The sender must belong to a configured team, and the forwarded sent message must
belong to the same team. Unauthorized, malformed, looped, or unreadable requests
produce no reply.

## Grant Log Read Access

The service user must read the Postfix log:

```sh
test -r /var/log/mail.log
sudo -u mailreceipt test -r /var/log/mail.log
```

On Debian-style systems `/var/log/mail.log` is often group-readable by `adm`.
One common setup is:

```sh
usermod -a -G adm mailreceipt
```

Log rotation must preserve readability. If `mailreceipt doctor --log
/var/log/mail.log` fails as the service user, fix the log ownership or ACL before
testing the pipe transport.

## Install The Wrapper

Install the production wrapper from this repository:

```sh
install -m 0755 contrib/postfix/mailreceipt-postfix /usr/local/libexec/mailreceipt-postfix
```

The wrapper defaults to quiet production behavior:

- no retained copy of the trigger message
- no retained stderr log
- no config hash, message hash, or byte-count diagnostics
- transient temp files are removed on exit

Debug capture is explicit. To enable it temporarily:

```sh
install -d -o mailreceipt -g mailreceipt -m 0700 /var/tmp/mailreceipt-debug
```

Then use either environment in `master.cf`:

```text
argv=/usr/bin/env MAILRECEIPT_POSTFIX_DEBUG=1 /usr/local/libexec/mailreceipt-postfix ${sender}
```

or the wrapper flag:

```text
argv=/usr/local/libexec/mailreceipt-postfix --debug ${sender}
```

Disable debug by removing the environment variable or `--debug` flag and reload
Postfix. List retained debug files after investigation:

```sh
find /var/tmp/mailreceipt-debug -type f \( -name 'in.*' -o -name 'err.*' \)
```

Review files before deleting them; debug input files contain full trigger
messages.

## Route The Receipt Address

Add `/etc/postfix/transport`:

```text
receipt@example.com mailreceipt:
```

Compile the transport map:

```sh
postmap /etc/postfix/transport
```

Ensure `main.cf` uses the map:

```text
transport_maps = hash:/etc/postfix/transport
```

If your Postfix setup rejects unknown local recipients before transport routing,
make sure `receipt@example.com` is accepted by your local recipient policy. For
example, add a local alias only when your site policy requires one:

```text
receipt: local-recipient-placeholder
```

Then run `newaliases`. The alias is only to pass recipient validation; transport
routing still sends `receipt@example.com` to the `mailreceipt:` transport.
Replace `local-recipient-placeholder` with a valid local recipient for your site.

## Add The Pipe Service

Add this service to `/etc/postfix/master.cf`:

```text
mailreceipt unix  -       n       n       -       -       pipe
    flags=q user=mailreceipt argv=/usr/local/libexec/mailreceipt-postfix ${sender}
```

Reload Postfix:

```sh
postfix check
postfix reload
```

Verify the route:

```sh
postmap -q receipt@example.com hash:/etc/postfix/transport
sendmail -bv receipt@example.com
```

The first command should print `mailreceipt:`. The second should report that the
address delivers to the wrapper command.

## Smoke Test The Filter

First replay a captured trigger message as the service user:

```sh
sudo -u mailreceipt sh -c '
  cd /etc/mailreceipt &&
  /usr/local/bin/mailreceipt filter \
    --envelope-from sender@example.com \
    --log /var/log/mail.log \
    < /path/to/trigger.eml \
    > /tmp/mailreceipt-reply.eml
'
```

If `/tmp/mailreceipt-reply.eml` is non-empty, send it through Postfix:

```sh
sudo -u mailreceipt /usr/sbin/sendmail -oi -t < /tmp/mailreceipt-reply.eml
```

Then send a real message to `receipt@example.com` with a sent-message `.eml`
attachment. Check both the transport and the reply path:

```sh
grep -i 'receipt@example.com' /var/log/mail.log | tail
grep -i 'mailreceipt-postfix' /var/log/syslog | tail
```

With debug enabled, inspect retained input and stderr files under
`/var/tmp/mailreceipt-debug`.

## Troubleshooting

| Symptom | Likely Cause | Check / Fix |
|---------|--------------|-------------|
| `unknown username: mailreceipt` | Postfix cannot resolve the service user | Create the `mailreceipt` service account, then `postfix reload`. |
| `User unknown in local recipient table` | Postfix rejected the recipient before transport routing | Confirm `transport_maps`, run `postmap`, and make the address acceptable to local recipient checks. |
| `mktemp ... Permission denied` in debug mode | Debug directory is not writable by the service user | `install -d -o mailreceipt -g mailreceipt -m 0700 /var/tmp/mailreceipt-debug`. |
| `status=sent (delivered via mailreceipt service)` but no reply | The wrapper succeeded but `mailreceipt filter` emitted no message | Replay the retained trigger in debug mode and confirm it contains a forwarded `.eml` sent message and an authorized envelope sender. |
| Debug log shows `out=0` | The filter intentionally failed closed | Check sender authorization, team ownership, loop headers, malformed input, unreadable attachments, and log readability. |
| Manual `sendmail -oi -t` works but the mail path does not | The wrapper path and the manual replay differ | Enable debug, compare the captured trigger message, and verify `${sender}` in `master.cf` is the authenticated envelope sender. |
| `sudo -u mailreceipt test -r /var/log/mail.log` fails | Service user cannot read the evidence log | Add the user to the log-readable group or configure an ACL/logrotate rule. |
| The reply says `not_found` for a message you expected | The forwarded message did not match the log | Prefer a real `.eml` / `message/rfc822` attachment so Message-ID correlation is preserved. |

## Security Boundary

The receipt address must not be exposed as an unauthenticated public inbound
mailbox. The wrapper trusts Postfix to pass the authenticated envelope sender as
`${sender}`. `mailreceipt` cannot authenticate SMTP by itself.

The generated receipt reports transport evidence only. It does not prove that a
person read the message, that a mailbox retained it, or that the receipt is
legally sufficient.
