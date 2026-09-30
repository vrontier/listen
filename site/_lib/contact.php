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
    $out = ['state' => $cfg ? 'form' : 'unconfigured', 'errors' => [], 'values' => ['name' => '', 'email' => '', 'message' => ''], 'token' => $cfg ? contact_token($cfg) : ''];
    if ($cfg === null || ($_SERVER['REQUEST_METHOD'] ?? 'GET') !== 'POST') {
        return $out;
    }

    $v = [];
    foreach (['name', 'email', 'message'] as $k) {
        $v[$k] = trim(str_replace("\r", '', (string) ($_POST[$k] ?? '')));
    }
    $out['values'] = $v;

    // Bots: the honeypot is invisible to people; pretend success.
    if (($_POST['website'] ?? '') !== '') {
        $out['state'] = 'sent';
        return $out;
    }
    if (!contact_token_ok($cfg, (string) ($_POST['token'] ?? ''))) {
        $out['errors']['form'] = 'The form expired or was sent too quickly. Please try again.';
    }
    if ($v['name'] === '' || mb_strlen($v['name']) > 120 || str_contains($v['name'], "\n")) {
        $out['errors']['name'] = 'Please give your name (up to 120 characters).';
    }
    if (!filter_var($v['email'], FILTER_VALIDATE_EMAIL) || mb_strlen($v['email']) > 200) {
        $out['errors']['email'] = 'Please give a valid email address, so we can reply.';
    }
    if (mb_strlen($v['message']) < 10 || mb_strlen($v['message']) > 5000) {
        $out['errors']['message'] = 'Please write a message between 10 and 5000 characters.';
    }
    if ($out['errors']) {
        return $out;
    }

    $subject = 'Listening Observatory: message from ' . $v['name'];
    $body = $v['message'] . "\n\n-- \n" . $v['name'] . ' <' . $v['email'] . ">\nsent via https://" . ($_SERVER['HTTP_HOST'] ?? SITE_HOST) . "/contact\n";
    $err = smtp_send($cfg, (string) $cfg['to'], $subject, $body, $v['name'], $v['email']);
    if ($err !== null) {
        error_log('contact: ' . $err);
        $out['errors']['form'] = 'The message could not be sent just now. Please try again later, or write to ' . $cfg['to'] . '.';
        return $out;
    }
    $out['state'] = 'sent';
    return $out;
}

/** Sends one plain-text mail; returns null on success or an error message. */
function smtp_send(array $cfg, string $to, string $subject, string $body, string $replyName, string $replyEmail): ?string
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
        'From: ' . $enc(SITE_NAME) . ' <' . $from . '>',
        'To: <' . $to . '>',
        'Reply-To: ' . $enc($replyName) . ' <' . $replyEmail . '>',
        'Subject: ' . $enc($subject),
        'Message-ID: <' . bin2hex(random_bytes(12)) . '@' . (strstr($from, '@') ? substr((string) strstr($from, '@'), 1) : 'listen') . '>',
        'MIME-Version: 1.0',
        'Content-Type: text/plain; charset=UTF-8',
        'Content-Transfer-Encoding: 8bit',
    ];
    $data = implode("\r\n", $headers) . "\r\n\r\n" . str_replace("\n", "\r\n", $body);
    $data = preg_replace('/^\./m', '..', $data); // dot-stuffing
    if ($e = $cmd($data . "\r\n.", 250)) { fclose($fp); return $e; }
    $cmd('QUIT', 221);
    fclose($fp);
    return null;
}
