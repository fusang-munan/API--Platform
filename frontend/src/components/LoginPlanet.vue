<template>
  <div class="planet-scene">
    <div class="planet-halo" />
    <div class="planet-globe">
      <div class="planet-surface" />
      <div class="planet-shade" />
    </div>
    <div class="planet-ring ring-outer"><span class="satellite satellite-small" /></div>
    <div class="planet-ring ring-main"><span class="satellite" /><span class="satellite satellite-trailing" /></div>
    <div class="planet-ring ring-inner" />
    <span class="distant-star star-one" />
    <span class="distant-star star-two" />
  </div>
</template>

<style scoped>
.planet-scene {
  position: relative;
  width: 590px;
  height: 410px;
}
.planet-halo {
  position: absolute;
  inset: -45px 5px -25px;
  border-radius: 50%;
  background: radial-gradient(ellipse, #b48bdd38 0%, #8060bc20 35%, transparent 67%);
}
.planet-globe {
  position: absolute;
  left: 151px;
  top: 63px;
  width: 270px;
  height: 270px;
  overflow: hidden;
  border-radius: 50%;
  background: #29213e;
  box-shadow: 0 0 3px #ecdaff, 0 0 28px #c1a0ed55, 0 0 90px #93749744;
}
.planet-surface {
  position: absolute;
  inset: -45%;
  border-radius: 50%;
  background:
    repeating-conic-gradient(from 30deg at 45% 48%, #cfb8d800 0deg, #cfb8d838 18deg, #47406c44 38deg, #cfb8d800 65deg),
    repeating-linear-gradient(156deg, #342749 0%, #645481 9%, #a787b8 13%, #68547d 18%, #cfb8d8 21%, #937497 25%, #47406c 32%);
  filter: blur(7px);
  transform: rotate(-18deg);
}
.planet-shade {
  position: absolute;
  inset: 0;
  border-radius: inherit;
  background: radial-gradient(circle at 73% 22%, #f1d8ff77, transparent 35%), linear-gradient(125deg, #0e0f1df5 6%, #0e0f1d88 32%, transparent 62%, #0e0f1d77);
  box-shadow: inset -7px 5px 12px #e4c5ff66, inset 24px -30px 45px #0e0f1d;
}
.planet-ring {
  position: absolute;
  left: 22px;
  top: 126px;
  width: 550px;
  height: 144px;
  border: 1px solid #cfb8d880;
  border-radius: 50%;
  transform: rotate(-29deg);
  box-shadow: 0 -2px 12px #cfb8d877, inset 0 2px 10px #cfb8d82b;
}
.ring-outer {
  left: 0;
  top: 107px;
  width: 594px;
  height: 182px;
  border-color: #b2a0dd40;
  box-shadow: 0 0 6px #cfb8d820;
}
.ring-inner {
  left: 47px;
  top: 145px;
  width: 500px;
  height: 104px;
  border-color: #e1c9f24d;
  box-shadow: 0 0 10px #cfb8d830;
}
.satellite {
  position: absolute;
  left: 0;
  top: 0;
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: #fff1ff;
  box-shadow: 0 0 9px 3px #edcfff, 0 0 26px 8px #c3a0ee88;
  offset-path: ellipse(50% 50% at 50% 50%);
  offset-distance: 12%;
}
.satellite-trailing { width: 4px; height: 4px; offset-distance: 60%; opacity: .55; }
.satellite-small { width: 4px; height: 4px; offset-distance: 35%; background: #cab5ff; }
.distant-star { position: absolute; width: 3px; height: 3px; border-radius: 50%; background: #e8d4ff; box-shadow: 0 0 12px 3px #b69ad477; }
.star-one { left: 95px; top: 65px; }
.star-two { right: 53px; bottom: 61px; }
@media (prefers-reduced-motion: no-preference) {
  .planet-scene { animation: planet-float 9s ease-in-out infinite; }
  .planet-surface { animation: planet-turn 38s linear infinite; }
  .planet-halo { animation: halo-breathe 7s ease-in-out infinite; }
  .ring-main { animation: ring-tilt 12s ease-in-out infinite; }
  .ring-outer { animation: ring-tilt 16s ease-in-out infinite reverse; }
  .satellite { animation: satellite-orbit 8s linear infinite; }
  .satellite-trailing { animation-delay: -4s; }
  .satellite-small { animation-duration: 13s; animation-direction: reverse; }
  .distant-star { animation: star-glow 4s ease-in-out infinite; }
  .star-two { animation-delay: -2s; }
}
@keyframes planet-float { 50% { transform: translateY(-12px) rotate(2deg); } }
@keyframes planet-turn { to { transform: rotate(342deg); } }
@keyframes halo-breathe { 50% { opacity: .55; transform: scale(.93); } }
@keyframes ring-tilt { 50% { transform: rotate(-23deg) scaleY(1.12); } }
@keyframes satellite-orbit { from { offset-distance: 0%; } to { offset-distance: 100%; } }
@keyframes star-glow { 50% { opacity: .25; } }
</style>
