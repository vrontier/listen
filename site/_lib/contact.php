<?php
declare(strict_types=1);

// Contact form: validation, spam protection and delivery by SMTP.
//
// Configuration lives in _config/mail.php (gitignored):
//
//   return [
//       'to'     => 'contact@vrontier.org',
//       'from'   => 'listen@vrontier.org',          // must be allowed by the SMTP account
//       'secret' => '<random string>',              // signs the form token
//       'smtp'   => ['host' => 'smtp.example.org', 'port' => 465, 'secure' => 'ssl',  // or 587 + 'tls'
//                    'user' => '...', 'pass' => '...'],
//   ];
//
// No sessions and no storage: a hidden honeypot field and a signed,
// time-limited token keep simple bots out.

function contact_config(): ?array
{
    $file = dirname(__DIR__) . '/_config/mail.php';
    if (!is_file($file)) {
        return null;
    }
    $cfg = require $file;
    return is_array($cfg) && !empty($cfg['secret']) && !empty($cfg['smtp']['host']) ? $cfg : null;
}

// token = "<unix time>.<hmac>" — must be at least 4 s and at most 2 h old.
function contact_token(array $cfg): string
{
    $t = (string) time();
    return $t . '.' . hash_hmac('sha256', 'contact|' . $t, (string) $cfg['secret']);
}

function contact_token_ok(array $cfg, string $token): bool
{
    [$t, $sig] = array_pad(explode('.', $token, 2), 2, '');
    if (!ctype_digit($t) || !hash_equals(hash_hmac('sha256', 'contact|' . $t, (string) $cfg['secret']), $sig)) {
        return false;
    }
    $age = time() - (int) $t;
    return $age >= 4 && $age <= 7200;
}

/** @return array{state: string, errors: array<string,string>, values: array<string,string>, token: string} */
function contact_handle(): array
{
    $cfg = contact_config();
    $out = ['state' => $cfg ? 'form' : 'unconfigured', 'errors' => [], 'values' => ['name' => '', 'email' => '', 'message' => '', 'copy' => ''], 'token' => $cfg ? contact_token($cfg) : ''];
    if ($cfg === null || ($_SERVER['REQUEST_METHOD'] ?? 'GET') !== 'POST') {
        return $out;
    }

    $v = [];
    foreach (['name', 'email', 'message'] as $k) {
        $v[$k] = trim(str_replace("\r", '', (string) ($_POST[$k] ?? '')));
    }
    $v['copy'] = ($_POST['copy'] ?? '') === '1' ? '1' : '';
    $out['values'] = $v;

    // Bots: the honeypot is invisible to people; pretend success.
    if (($_POST['website'] ?? '') !== '') {
        $out['state'] = 'sent';
        return $out;
    }
    if (!contact_token_ok($cfg, (string) ($_POST['token'] ?? ''))) {
        $out['errors']['form'] = 'The form expired or was sent too quickly. Please try again.';
    }
    if ($v['name'] === '') {
        $out['errors']['name'] = 'Please give your name.';
    } elseif (mb_strlen($v['name']) > 120 || str_contains($v['name'], "\n")) {
        $out['errors']['name'] = 'Please give your name on one line (up to 120 characters).';
    }
    if (!filter_var($v['email'], FILTER_VALIDATE_EMAIL) || mb_strlen($v['email']) > 200) {
        $out['errors']['email'] = 'Please give a valid email address, so we can reply.';
    }
    if ($v['message'] === '') {
        $out['errors']['message'] = 'Please write a message.';
    } elseif (mb_strlen($v['message']) > 5000) {
        $out['errors']['message'] = 'Please keep the message under 5000 characters.';
    }
    if ($out['errors']) {
        return $out;
    }
    // A copy goes to an address the visitor typed in, so limit how often
    // any one address can make the server send mail.
    if (!contact_rate_ok($cfg, (string) ($_SERVER['REMOTE_ADDR'] ?? ''))) {
        $out['errors']['form'] = 'Too many messages from your connection in the last hour. Please try again later, or write to ' . $cfg['to'] . '.';
        return $out;
    }

    $site = 'https://' . ($_SERVER['HTTP_HOST'] ?? SITE_HOST);
    $when = (new DateTimeImmutable('now', new DateTimeZone('Europe/Berlin')))->format('j F Y, H:i T');
    [$text, $html] = contact_mail_notification($v, $site, $when);
    $err = smtp_send($cfg, (string) $cfg['to'], 'Message from ' . $v['name'], $text, $v['name'], $v['email'], $html);
    if ($err !== null) {
        error_log('contact: ' . $err);
        $out['errors']['form'] = 'The message could not be sent just now. Please try again later, or write to ' . $cfg['to'] . '.';
        return $out;
    }
    if ($v['copy'] === '1') {
        [$text, $html] = contact_mail_copy($v, $site, $when);
        $err = smtp_send($cfg, $v['email'], 'Your message to ' . SITE_NAME, $text, SITE_NAME, (string) $cfg['to'], $html);
        if ($err !== null) {
            error_log('contact copy: ' . $err);
            $out['state'] = 'sent-nocopy';
            return $out;
        }
    }
    $out['state'] = 'sent';
    return $out;
}

// At most 5 submissions per hour per client address, counted in a small file
// in state_dir (writable by PHP). Without a state_dir there is no limit.
function contact_rate_ok(array $cfg, string $ip): bool
{
    $dir = (string) ($cfg['state_dir'] ?? '');
    if ($dir === '' || !is_dir($dir) || !is_writable($dir)) {
        return true;
    }
    $file = $dir . '/contact-rate.json';
    $fp = fopen($file, 'c+');
    if (!$fp) {
        return true;
    }
    flock($fp, LOCK_EX);
    $data = json_decode((string) stream_get_contents($fp), true) ?: [];
    $now = time();
    $key = hash('sha256', $ip . '|' . $cfg['secret']); // no plain addresses stored
    foreach ($data as $k => $times) {
        $data[$k] = array_values(array_filter((array) $times, fn ($t) => $t > $now - 3600));
        if (!$data[$k]) {
            unset($data[$k]);
        }
    }
    $ok = count($data[$key] ?? []) < 5;
    if ($ok) {
        $data[$key][] = $now;
    }
    ftruncate($fp, 0);
    rewind($fp);
    fwrite($fp, (string) json_encode($data));
    flock($fp, LOCK_UN);
    fclose($fp);
    return $ok;
}

/** Sends one plain-text mail; returns null on success or an error message. */
function smtp_send(array $cfg, string $to, string $subject, string $body, string $replyName, string $replyEmail, ?string $html = null): ?string
{
    $s = $cfg['smtp'];
    $secure = $s['secure'] ?? 'ssl';
    $host = (string) $s['host'];
    $port = (int) ($s['port'] ?? ($secure === 'ssl' ? 465 : 587));
    $ctx = stream_context_create(['ssl' => ['peer_name' => $host, 'verify_peer' => true, 'verify_peer_name' => true]]);
    $fp = @stream_socket_client(($secure === 'ssl' ? 'ssl://' : 'tcp://') . $host . ':' . $port, $errno, $errstr, 15, STREAM_CLIENT_CONNECT, $ctx);
    if (!$fp) {
        return "connect $host:$port: $errstr";
    }
    stream_set_timeout($fp, 15);

    $read = function () use ($fp): array {
        $lines = '';
        while (($line = fgets($fp, 1024)) !== false) {
            $lines .= $line;
            if (strlen($line) < 4 || $line[3] === ' ') {
                break;
            }
        }
        return [(int) substr($lines, 0, 3), trim($lines)];
    };
    $cmd = function (string $c, int $want) use ($fp, $read): ?string {
        fwrite($fp, $c . "\r\n");
        [$code, $text] = $read();
        return $code === $want ? null : 'SMTP ' . (str_starts_with($c, 'AUTH') || strlen($c) > 40 ? explode(' ', $c)[0] : $c) . ': ' . $text;
    };

    $from = (string) $cfg['from'];
    $local = gethostname() ?: 'listen';
    if ((($r = $read())[0]) !== 220) { fclose($fp); return 'greeting: ' . $r[1]; }
    if ($e = $cmd('EHLO ' . $local, 250)) { fclose($fp); return $e; }
    if ($secure === 'tls') {
        if ($e = $cmd('STARTTLS', 220)) { fclose($fp); return $e; }
        if (!stream_socket_enable_crypto($fp, true, STREAM_CRYPTO_METHOD_TLS_CLIENT)) { fclose($fp); return 'STARTTLS failed'; }
        if ($e = $cmd('EHLO ' . $local, 250)) { fclose($fp); return $e; }
    }
    if (!empty($s['user'])) {
        if ($e = $cmd('AUTH LOGIN', 334)) { fclose($fp); return $e; }
        if ($e = $cmd(base64_encode((string) $s['user']), 334)) { fclose($fp); return 'SMTP AUTH user rejected'; }
        if ($e = $cmd(base64_encode((string) $s['pass']), 235)) { fclose($fp); return 'SMTP AUTH password rejected'; }
    }
    if ($e = $cmd('MAIL FROM:<' . $from . '>', 250)) { fclose($fp); return $e; }
    if ($e = $cmd('RCPT TO:<' . $to . '>', 250)) { fclose($fp); return $e; }
    if ($e = $cmd('DATA', 354)) { fclose($fp); return $e; }

    $enc = fn (string $t) => '=?UTF-8?B?' . base64_encode($t) . '?=';
    $headers = [
        'Date: ' . date(DATE_RFC2822),
        'From: ' . $enc((string) ($cfg['from_name'] ?? SITE_NAME)) . ' <' . $from . '>',
        'To: <' . $to . '>',
        'Reply-To: ' . $enc($replyName) . ' <' . $replyEmail . '>',
        'Subject: ' . $enc($subject),
        'Message-ID: <' . bin2hex(random_bytes(12)) . '@' . (strstr($from, '@') ? substr((string) strstr($from, '@'), 1) : 'listen') . '>',
        'MIME-Version: 1.0',
    ];
    $crlf = fn (string $t) => str_replace("\n", "\r\n", str_replace("\r\n", "\n", $t));
    if ($html === null) {
        $headers[] = 'Content-Type: text/plain; charset=UTF-8';
        $headers[] = 'Content-Transfer-Encoding: 8bit';
        $payload = $crlf($body);
    } else {
        // Plain text for clients without HTML, HTML for the rest; both
        // quoted-printable so no line is too long for SMTP.
        $b = 'lo-' . bin2hex(random_bytes(12));
        $headers[] = 'Content-Type: multipart/alternative; boundary="' . $b . '"';
        $part = fn (string $type, string $content) => "--$b\r\nContent-Type: $type; charset=UTF-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n"
            . quoted_printable_encode($crlf($content)) . "\r\n";
        $payload = $part('text/plain', $body) . $part('text/html', $html) . "--$b--";
    }
    $data = implode("\r\n", $headers) . "\r\n\r\n" . $payload;
    $data = preg_replace('/^\./m', '..', $data); // dot-stuffing
    if ($e = $cmd($data . "\r\n.", 250)) { fclose($fp); return $e; }
    $cmd('QUIT', 221);
    fclose($fp);
    return null;
}

// ---- mail templates ----------------------------------------------------------
//
// Simple and clean, in the spirit of the site: a small letter-spaced
// wordmark with an orange rule, the facts in a compact table, the message in
// a block with a warm left border. Light background, tables and inline
// styles, which every mail client renders. All visitor text is escaped.

/** @return array{0: string, 1: string} plain text and HTML */
function contact_mail_notification(array $v, string $site, string $when): array
{
    $text = "New message via the contact form\n\n"
        . "From:    {$v['name']} <{$v['email']}>\n"
        . "Sent:    $when\n"
        . "Via:     $site/contact\n\n"
        . str_repeat('-', 60) . "\n\n" . $v['message'] . "\n\n" . str_repeat('-', 60) . "\n"
        . "Reply to this email to answer {$v['name']} directly.\n";

    $h = fn (string $t) => htmlspecialchars($t, ENT_QUOTES, 'UTF-8');
    $reply = 'mailto:' . rawurlencode($v['email']) . '?subject=' . rawurlencode('Re: your message to ' . SITE_NAME);
    $html = contact_mail_frame(
        'New message',
        'Someone wrote to you through the contact form.',
        contact_mail_rows([
            'From' => '<strong style="font-weight:600;color:#1b1c1e">' . $h($v['name']) . '</strong><br><a href="mailto:' . $h($v['email']) . '" style="color:#b8572f;text-decoration:none">' . $h($v['email']) . '</a>',
            'Sent' => $h($when),
            'Via'  => '<a href="' . $h($site) . '/contact" style="color:#6c6f73;text-decoration:none">' . $h((string) preg_replace('#^https?://#', '', $site)) . '/contact</a>',
        ])
        . contact_mail_message($v['message'])
        . '<tr><td style="padding:8px 0 4px"><a href="' . $h($reply) . '" style="display:inline-block;padding:11px 20px;border:1px solid #e0784f;color:#b8572f;font-family:Menlo,Consolas,monospace;font-size:12px;letter-spacing:2px;text-transform:uppercase;text-decoration:none">Reply to ' . $h($v['name']) . '</a></td></tr>',
        'Replying to this email answers ' . $h($v['name']) . ' directly.'
    );
    return [$text, $html];
}

/** @return array{0: string, 1: string} plain text and HTML */
function contact_mail_copy(array $v, string $site, string $when): array
{
    $text = "Thank you for your message.\n\n"
        . "This is a copy of what you sent to " . SITE_NAME . " on $when.\n"
        . "We will reply to this address.\n\n"
        . str_repeat('-', 60) . "\n\n" . $v['message'] . "\n\n" . str_repeat('-', 60) . "\n"
        . SITE_NAME . " · $site\n";

    $h = fn (string $t) => htmlspecialchars($t, ENT_QUOTES, 'UTF-8');
    $html = contact_mail_frame(
        'Thank you, ' . $h($v['name']),
        'This is a copy of the message you sent to ' . SITE_NAME . ' on ' . $h($when) . '. We will reply to this address.',
        contact_mail_message($v['message']),
        'You receive this copy because you asked for it on <a href="' . $h($site) . '/contact" style="color:#6c6f73">' . $h((string) preg_replace('#^https?://#', '', $site)) . '</a>.'
    );
    return [$text, $html];
}

function contact_mail_rows(array $rows): string
{
    $out = '<tr><td style="padding:0 0 22px"><table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="border-collapse:collapse">';
    foreach ($rows as $label => $value) {
        $out .= '<tr><td valign="top" style="width:64px;padding:7px 12px 7px 0;border-top:1px solid #ecebe7;font-family:Menlo,Consolas,monospace;font-size:11px;letter-spacing:1.5px;text-transform:uppercase;color:#8b8e92">' . $label . '</td>'
            . '<td style="padding:7px 0;border-top:1px solid #ecebe7;font-size:15px;line-height:1.5;color:#3b3d40">' . $value . '</td></tr>';
    }
    return $out . '</table></td></tr>';
}

function contact_mail_message(string $message): string
{
    $body = nl2br(htmlspecialchars($message, ENT_QUOTES, 'UTF-8'), false);
    return '<tr><td style="padding:0 0 24px"><div style="border-left:3px solid #f0b26e;background:#faf8f4;padding:16px 18px;font-size:15px;line-height:1.65;color:#1b1c1e;word-break:break-word">'
        . $body . '</div></td></tr>';
}

function contact_mail_frame(string $title, string $intro, string $content, string $foot): string
{
    return '<!DOCTYPE html><html lang="en"><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1">'
        . '<meta name="color-scheme" content="light"><title>' . strip_tags($title) . '</title></head>'
        . '<body style="margin:0;padding:0;background:#f3f2ee">'
        . '<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background:#f3f2ee"><tr><td align="center" style="padding:32px 16px">'
        . '<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="max-width:560px;background:#ffffff;border:1px solid #e6e4de;font-family:-apple-system,BlinkMacSystemFont,\'Segoe UI\',Helvetica,Arial,sans-serif">'
        // wordmark with a short orange rule
        . '<tr><td style="padding:26px 32px 0"><div style="font-family:Menlo,Consolas,monospace;font-size:12px;letter-spacing:4px;text-transform:uppercase;color:#1b1c1e">Listening Observatory</div>'
        . '<div style="width:36px;height:2px;background:#e0784f;margin:12px 0 0;line-height:2px;font-size:0">&nbsp;</div></td></tr>'
        // title, intro, content
        . '<tr><td style="padding:26px 32px 8px"><table role="presentation" width="100%" cellpadding="0" cellspacing="0">'
        . '<tr><td style="padding:0 0 8px;font-size:22px;font-weight:600;line-height:1.3;color:#1b1c1e">' . $title . '</td></tr>'
        . '<tr><td style="padding:0 0 22px;font-size:15px;line-height:1.6;color:#6c6f73">' . $intro . '</td></tr>'
        . $content
        . '</table></td></tr>'
        // footer
        . '<tr><td style="padding:18px 32px 24px;border-top:1px solid #ecebe7;font-size:12px;line-height:1.6;color:#8b8e92">' . $foot
        . '<br><span style="font-family:Menlo,Consolas,monospace;letter-spacing:1px">' . SITE_HOST . ' · A Vrontier project</span></td></tr>'
        . '</table></td></tr></table></body></html>';
}
