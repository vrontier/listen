<?php
declare(strict_types=1);

const SITE_NAME = 'Listening Observatory';
const SITE_HOST = 'listen.vrontier.org';

// Fixed pages: path => view + page metadata. Each source in
// _config/sources.json adds /<slug> (see route()).
const PAGES = [
    '/' => [
        'view'        => 'home',
        'title'       => SITE_NAME,
        'description' => 'An experimental system for continuous computational listening.',
        'scripts'     => ['js/home.js'],
    ],
    '/contact' => [
        'view'        => 'contact',
        'title'       => 'Contact',
        'description' => 'Get in touch with the Listening Observatory.',
    ],
];

// ---- sources ---------------------------------------------------------------

// The streams the observatory listens to, from _config/sources.json
// (gitignored; see sources.example.json). Without it there are no streams.
function sources_config(): array
{
    static $cfg = null;
    if ($cfg !== null) {
        return $cfg;
    }
    $cfg  = ['default' => '', 'sources' => []];
    $file = dirname(__DIR__) . '/_config/sources.json';
    if (is_file($file)) {
        $data = json_decode((string) file_get_contents($file), true);
        if (is_array($data) && isset($data['sources']) && is_array($data['sources'])) {
            $cfg = $data + $cfg;
        }
    }
    return $cfg;
}

function source(string $slug): ?array
{
    foreach (sources_config()['sources'] as $s) {
        if (($s['slug'] ?? '') === $slug) {
            return $s + [
                'listed'   => false,
                'name'     => $slug,
                'subtitle' => 'Live listening',
                'meta'     => [],
                'timezone' => 'UTC',
                'summary'  => '',
                'credit'   => null,
                'audio'    => false,
            ];
        }
    }
    return null;
}

/** @return array<int, array> the sources shown on the landing page */
function listed_sources(): array
{
    $out = [];
    foreach (sources_config()['sources'] as $s) {
        if (!empty($s['listed']) && ($src = source((string) $s['slug'])) !== null) {
            $out[] = $src;
        }
    }
    return $out;
}

// Where the page reaches a source's listener. Normally through NGINX under
// /<slug>/ws/live; in local development (LISTEN_DIRECT_PORTS=1, set by
// scripts/dev.sh) straight at the listener's port.
function live_ws_url(array $source): string
{
    if (getenv('LISTEN_DIRECT_PORTS') === '1' && isset($source['listener']['port'])) {
        return 'ws://127.0.0.1:' . (int) $source['listener']['port'] . '/ws/live';
    }
    return '/' . $source['slug'] . '/ws/live';
}

// Base URL of a source's listener API (for the stream cards' live status).
function live_api_base(array $source): string
{
    if (getenv('LISTEN_DIRECT_PORTS') === '1' && isset($source['listener']['port'])) {
        return 'http://127.0.0.1:' . (int) $source['listener']['port'] . '/api';
    }
    return '/' . $source['slug'] . '/api';
}

// ---- routing ---------------------------------------------------------------

/** Resolves a path to a page, a redirect ['redirect' => url], or null (404). */
function route(string $path): ?array
{
    if (isset(PAGES[$path])) {
        return PAGES[$path];
    }
    if ($path === '/live') {
        $default = (string) (sources_config()['default'] ?? '');
        return $default !== '' ? ['redirect' => '/' . $default] : null;
    }
    if (preg_match('#^/([a-z0-9][a-z0-9-]{0,62})/?$#', $path, $m) && ($src = source($m[1])) !== null) {
        return [
            'view'        => 'live',
            'layout'      => 'layout-live',
            'title'       => $src['subtitle'],
            'description' => $src['summary'] !== '' ? $src['summary'] : 'A live computational listening of ' . $src['name'] . '.',
            'source'      => $src,
            'noindex'     => empty($src['listed']),
        ];
    }
    return null;
}

function request_path(): string
{
    $path = parse_url($_SERVER['REQUEST_URI'] ?? '/', PHP_URL_PATH);
    return is_string($path) && $path !== '' ? $path : '/';
}

// ---- rendering -------------------------------------------------------------

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
