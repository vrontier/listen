<?php /** @var array $page */ $streams = listed_sources(); ?>
    <section class="page__hero">
      <p class="page__eyebrow">Experimental · in development</p>
      <h1>Listening Observatory</h1>
      <p class="page__lede">An experimental system for continuous computational listening.</p>
      <p class="page__intro">
        Listening Observatory connects to live audio streams, analyses their acoustic structure
        in real time, and translates what it hears into evolving visual forms and short textual
        observations. Rather than treating sound as something to be displayed moment by moment,
        it listens across timescales: from immediate frequency changes to recurring resonances,
        motifs, and longer patterns it remembers.
      </p>
    </section>

    <section class="page__section" id="streams" aria-labelledby="streams-h">
      <h2 id="streams-h">Streams</h2>
<?php if (!$streams): ?>
      <p class="page__muted">No streams are configured yet.</p>
<?php else: ?>
      <p class="streams__now" id="streams-now" aria-live="polite" hidden></p>
      <div class="streams">
<?php foreach ($streams as $s): ?>
        <a class="stream" href="/<?= e($s['slug']) ?>" data-api="<?= e(live_api_base($s)) ?>">
          <span class="stream__status" data-state="connecting"><span class="stream__dot"></span><span class="stream__text">checking…</span><span class="stream__audience"></span></span>
          <span class="stream__name"><?= e($s['name']) ?></span>
          <span class="stream__meta"><?= e(implode(' · ', array_slice($s['meta'], 0, 2))) ?></span>
          <span class="stream__summary"><?= e($s['summary']) ?></span>
          <span class="stream__open">Listen →</span>
        </a>
<?php endforeach; ?>
      </div>
<?php endif; ?>
    </section>

    <section class="page__section" aria-labelledby="concept-h">
      <h2 id="concept-h">How it listens</h2>
      <div class="layers4">
        <article>
          <h3>Observation</h3>
          <p>Signal processing measures the sound continuously: frequencies, harmonics, spectral
             energy, entropy, sudden transients and persistent resonances.</p>
        </article>
        <article>
          <h3>Memory</h3>
          <p>A memory compares what it hears with what it has heard before. Structures that recur
             become <em>motifs</em> with a persistent identity, and are recognised when they return.</p>
        </article>
        <article>
          <h3>Visualization</h3>
          <p>Drawn live in the browser from these observations: a landscape of the spectrum,
             resonances rising from it, disturbances where transients strike, and a dial of what
             has been remembered.</p>
        </article>
        <article>
          <h3>Interpretation</h3>
          <p>A language model phrases the measured facts in two short sentences. It never decides
             what happened in the sound: every frequency and motif it names is checked against
             the measurements, or the text is discarded.</p>
        </article>
      </div>
      <p class="page__principle">The DSP system observes · the memory recognises · the visualization
        embodies · the language model gives words to what was measured.</p>
    </section>

    <section class="page__section" id="reading" aria-labelledby="reading-h">
      <h2 id="reading-h">How to read a live page</h2>
      <div class="reading">
        <figure class="schematic" aria-hidden="true">
          <div class="schematic__stage">
            <span class="n n1">1</span><span class="n n2">2</span><span class="n n3">3</span>
            <span class="n n4">4</span><span class="n n5">5</span>
          </div>
          <div class="schematic__line"><span class="n n6">6</span></div>
          <div class="schematic__dial"><span class="n n7">7</span></div>
          <div class="schematic__panels"><span class="n n8">8</span><span class="n n9">9</span></div>
        </figure>
        <ol class="legend">
          <li><b>The landscape.</b> The spectrum as terrain: low frequencies on the left in warm
            colours, high ones on the right in cool colours. Height is loudness; the rows recede
            into the past, the front row is now.</li>
          <li><b>Resonances.</b> Spires rise where a frequency holds steadily. The label gives
            its frequency and, once remembered, its motif; rings mean it has harmonics, thinner
            spires mark the harmonics themselves.</li>
          <li><b>Transients.</b> A sudden burst of sound sends a shockwave through the landscape.</li>
          <li><b>Readout.</b> The time at the source, the overall state (for example
            <i>stable resonance</i> or <i>broadband noise</i>), harmonicity, spectral entropy and
            novelty: how different the sound is from the last minutes.</li>
          <li><b>Listen.</b> Plays the stream in step with the picture: every event is shown when
            you hear it. Where a source allows it.</li>
          <li><b>Interpretation.</b> The newest text types in on this line. Scroll it (wheel,
            trackpad or swipe) to read earlier ones.</li>
          <li><b>Memory dial.</b> Every remembered motif is a station on a radio dial, at its
            frequency: taller marks were heard more often, brighter ones more recently, shaded
            bands are sound textures. The needle follows the dominant frequency. Point at a mark
            for its details.</li>
          <li><b>Signal.</b> Spectrogram of the last minute, the current spectrum with its peaks,
            and the harmonic map of the last five minutes, coloured by motif.</li>
          <li><b>Observations.</b> The measured features and the latest events: resonances,
            fades, transients, new and returning motifs.</li>
        </ol>
      </div>
    </section>

    <section class="page__section" aria-labelledby="more-h">
      <h2 id="more-h">More</h2>
      <p class="page__muted">
        Listening Observatory is open source (<a href="https://github.com/vrontier/listen" target="_blank" rel="noopener noreferrer">github.com/vrontier/listen</a>).
        The audio belongs to its sources and is credited on each stream's page.
        Questions, ideas or a stream worth listening to? <a href="/contact">Get in touch.</a>
      </p>
    </section>
