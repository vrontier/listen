<?php
/** @var array $page */
/** @var string $content */
// Full-bleed layout for the live visualization: no site chrome, no ambient
// placeholder canvas. p5.js is vendored and pinned (assets/vendor/p5/VERSION).
$source      = $page['source'];
$title       = $source['name'] === $page['title'] ? $page['title'] : $source['name'] . ' · ' . $page['title'];
$fullTitle   = $title . ' · ' . SITE_NAME;
$description = $page['description'] ?? '';
?>
<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title><?= e($fullTitle) ?></title>
  <meta name="description" content="<?= e($description) ?>">
<?php if (!empty($page['noindex'])): ?>
  <meta name="robots" content="noindex, nofollow">
<?php endif; ?>
  <meta property="og:title" content="<?= e($fullTitle) ?>">
  <meta property="og:description" content="<?= e($description) ?>">
  <meta property="og:url" content="https://<?= e(SITE_HOST) ?>/<?= e($source['slug']) ?>">
  <!-- Link previews (text only; the site has no preview image). -->
  <meta property="og:type" content="website">
  <meta property="og:site_name" content="<?= e(SITE_NAME) ?>">
  <meta name="twitter:card" content="summary">
  <meta name="theme-color" content="#07080a">
  <link rel="icon" href="<?= e(asset('favicon.svg')) ?>" type="image/svg+xml">
  <link rel="stylesheet" href="<?= e(asset('css/site.css')) ?>">
  <link rel="stylesheet" href="<?= e(asset('css/live.css')) ?>">
</head>
<body class="live">
<?= $content ?>
  <script src="<?= e(asset('vendor/p5/p5.min.js')) ?>"
          integrity="sha384-Cs48F1uukMPysq29xNsf/FZL5ZNGsPfi6lDSGOxo6dypVFFiWO9Q3YbRKoXPPBii"
          crossorigin="anonymous" defer></script>
<?php foreach (['stream', 'state', 'field', 'dial', 'panels', 'audio', 'knob', 'jog', 'main'] as $js): ?>
  <script src="<?= e(asset('js/live/' . $js . '.js')) ?>" defer></script>
<?php endforeach; ?>
</body>
</html>
