<?php
// Copy to source.php (gitignored) to name the analysed source on /live.
// Every key is optional; missing keys fall back to the neutral defaults in
// _lib/bootstrap.php.
return [
    'name'     => 'Example Stream',
    'subtitle' => 'Live listening',
    'meta'     => ['Somewhere, CA', 'Wind, environment', 'Live audio stream', 'Computational interpretation'],
    'timezone' => 'America/Los_Angeles', // local time of the source (IANA name)
    'credit'   => [
        'text' => 'Example Studio — Example Stream',
        'url'  => 'https://example.org/stream',
        'note' => 'live feed from the example site',
    ],
];
