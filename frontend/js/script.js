document.addEventListener('DOMContentLoaded', function() {
  const input = document.getElementById('urlInput');
  if (input) {
    input.focus();
    input.addEventListener('keydown', function(e) {
      if (e.key === 'Enter') {
        this.form.submit();
      }
    });
  }
});
