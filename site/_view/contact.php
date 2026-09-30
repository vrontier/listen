<?php
/** @var array $page */
$f = $page['form'];
$v = $f['values'];
$err = $f['errors'];
?>
    <section class="page__hero">
      <p class="page__eyebrow">Contact</p>
      <h1>Get in touch</h1>
      <p class="page__lede">Questions, ideas, or a stream worth listening to.</p>
    </section>

    <section class="page__section">
<?php if ($f['state'] === 'sent'): ?>
      <p class="notice notice--ok" role="status">Thank you. Your message is on its way; we will reply by email.</p>
<?php elseif ($f['state'] === 'unconfigured'): ?>
      <p class="notice" role="status">The form isn't connected yet. Please write to
        <a href="mailto:contact@vrontier.org">contact@vrontier.org</a> in the meantime.</p>
<?php else: ?>
<?php if (!empty($err['form'])): ?>
      <p class="notice notice--error" role="alert"><?= e($err['form']) ?></p>
<?php endif; ?>
      <form class="contact" method="post" action="/contact" novalidate>
        <input type="hidden" name="token" value="<?= e($f['token']) ?>">
        <div class="contact__trap" aria-hidden="true">
          <label>Website <input type="text" name="website" tabindex="-1" autocomplete="off"></label>
        </div>
        <label class="field">
          <span>Name</span>
          <input type="text" name="name" maxlength="120" autocomplete="name" required value="<?= e($v['name']) ?>"<?= isset($err['name']) ? ' aria-invalid="true" aria-describedby="e-name"' : '' ?>>
<?php if (isset($err['name'])): ?><small id="e-name"><?= e($err['name']) ?></small><?php endif; ?>
        </label>
        <label class="field">
          <span>Email</span>
          <input type="email" name="email" maxlength="200" autocomplete="email" required value="<?= e($v['email']) ?>"<?= isset($err['email']) ? ' aria-invalid="true" aria-describedby="e-email"' : '' ?>>
<?php if (isset($err['email'])): ?><small id="e-email"><?= e($err['email']) ?></small><?php endif; ?>
        </label>
        <label class="field">
          <span>Message</span>
          <textarea name="message" rows="8" maxlength="5000" required<?= isset($err['message']) ? ' aria-invalid="true" aria-describedby="e-message"' : '' ?>><?= e($v['message']) ?></textarea>
<?php if (isset($err['message'])): ?><small id="e-message"><?= e($err['message']) ?></small><?php endif; ?>
        </label>
        <p class="contact__note">Your message goes to contact@vrontier.org. We use your address only to reply.</p>
        <button type="submit" class="button">Send message</button>
      </form>
<?php endif; ?>
    </section>
