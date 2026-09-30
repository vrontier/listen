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
];

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
    require dirname(__DIR__) . '/_view/layout.php';
}
