(function () {
  document.documentElement.classList.add('js');

  var form = document.querySelector('form');
  if (!form) {
    return;
  }

  var first = form.querySelector('input[type=text], input[type=email]');
  if (first) {
    first.focus({ preventScroll: true });
  }

  form.addEventListener('submit', function () {
    var button = form.querySelector('button[type=submit]');
    if (button) {
      button.disabled = true;
    }
  });
})();
