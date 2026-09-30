<?php
// Copy to mail.php (gitignored) to enable the contact form.
return [
    'to'     => 'contact@vrontier.org',
    'from'   => 'listen@vrontier.org',        // an address the SMTP account may send as
    'secret' => 'replace-with-a-long-random-string',
    'smtp'   => [
        'host'   => 'smtp.example.org',
        'port'   => 465,
        'secure' => 'ssl',                    // 'ssl' (port 465) or 'tls' (STARTTLS, port 587)
        'user'   => 'listen@vrontier.org',
        'pass'   => '...',
    ],
];
