<?php
declare(strict_types=1);

const SITE_NAME = 'Listening Observatory';
const SITE_HOST = 'listen.vrontier.org';

// Exact-match routes: path => view + page metadata.
const ROUTES = [
    '/' => [
        'view'        => 'home',
        'title'       => SITE_NAME,
        'description' => 'An experimental system for continuous computational listening.',
    ],
    '/live' => [
        'view'        => 'live',
        'layout'      => 'layout-live',
        'title'       => 'Live listening',
        'description' => 'A live computational listening of a continuous environmental audio stream.',
    ],
];

// WebSocket endpoint of the listener daemon. Empty means same host, /ws/live
// (NGINX proxies it). Set LISTEN_WS_URL for other setups; ?ws= overrides it
// in the browser during development.
function live_ws_url(): string
{
    $url = getenv('LISTEN_WS_URL');
    return is_string($url) ? $url : '';
}

// Naming and credit of the analysed source on /live. The public defaults are
// neutral; a deployment names its source in _config/source.php (gitignored,
// see _config/source.example.php).
function live_source(): array
{
    $source = [
        'name'     => 'Live listening',
        'subtitle' => 'Computational listening',
        'meta'     => ['Live audio stream', 'Computational interpretation'],
        'timezone' => 'UTC',
        'credit'   => null, // ['text' => …, 'url' => …, 'note' => …]
        // Offer in-sync playback of the relayed audio (the listener must run
        // with -audio as well). Only where the source's terms allow it.
        'audio'    => false,
    ];
    $file = dirname(__DIR__) . '/_config/source.php';
    if (is_file($file)) {
        $local = require $file;
        if (is_array($local)) {
            $source = array_merge($source, $local);
        }
    }
    return $source;
}

function request_path(): string
{
    $path = parse_url($_SERVER['REQUEST_URI'] ?? '/', PHP_URL_PATH);
    return is_string($path) && $path !== '' ? $path : '/';
}

function e(string $s): string
{
    return htmlspecialchars($s, ENT_QUOTES | ENT_SUBSTITUTE, 'UTF-8');
}

function asset(string $rel): string
{
    // Cache-bust by mtime; assets are served with a long-lived immutable header.
    $file = dirname(__DIR__) . '/assets/' . $rel;
    $v    = is_file($file) ? (string) filemtime($file) : '0';
    return '/assets/' . $rel . '?v=' . $v;
}

function render(string $view, array $page): void
{
    $viewFile = dirname(__DIR__) . '/_view/' . $view . '.php';
    ob_start();
    require $viewFile;
    $content = ob_get_clean();
    $layout = $page['layout'] ?? 'layout';
    require dirname(__DIR__) . '/_view/' . $layout . '.php';
}
