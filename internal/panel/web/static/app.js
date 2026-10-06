"use strict";

(() => {
  let toastTimer;

  function notify(message) {
    const toast = document.getElementById("toast");
    if (!toast) return;
    window.clearTimeout(toastTimer);
    toast.textContent = message;
    toast.hidden = false;
    toastTimer = window.setTimeout(() => { toast.hidden = true; }, 4500);
  }

  document.querySelectorAll("form").forEach((form) => {
    form.addEventListener("submit", (event) => {
      const message = form.dataset.confirm;
      if (message && !window.confirm(message)) {
        event.preventDefault();
        return;
      }
      if (form.dataset.submitting === "true") {
        event.preventDefault();
        return;
      }
      form.dataset.submitting = "true";
      form.setAttribute("aria-busy", "true");
      const submitter = event.submitter;
      if (submitter) {
        submitter.disabled = true;
        submitter.dataset.originalLabel = submitter.innerHTML;
        if (!submitter.classList.contains("icon-button")) submitter.textContent = "Подождите…";
      }
    });
  });

  window.addEventListener("pageshow", () => {
    document.querySelectorAll("form[data-submitting]").forEach((form) => {
      delete form.dataset.submitting;
      form.removeAttribute("aria-busy");
      form.querySelectorAll("[data-original-label]").forEach((button) => {
        button.disabled = false;
        button.innerHTML = button.dataset.originalLabel;
        delete button.dataset.originalLabel;
      });
    });
  });

  document.querySelectorAll("[data-toggle-password]").forEach((button) => {
    button.addEventListener("click", () => {
      const input = document.getElementById(button.dataset.togglePassword);
      if (!input) return;
      const reveal = input.type === "password";
      input.type = reveal ? "text" : "password";
      button.textContent = reveal ? "Скрыть" : "Показать";
      button.setAttribute("aria-label", reveal ? "Скрыть пароль" : "Показать пароль");
      button.setAttribute("aria-pressed", String(reveal));
    });
  });

  document.querySelectorAll("[data-generate-password]").forEach((button) => {
    button.addEventListener("click", () => {
      const input = document.getElementById(button.dataset.generatePassword);
      if (!input) return;
      if (!window.crypto || !window.crypto.getRandomValues) {
        notify("Генератор недоступен в этом браузере. Задайте пароль вручную.");
        return;
      }
      const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_-";
      const random = new Uint8Array(24);
      window.crypto.getRandomValues(random);
      input.value = Array.from(random, (value) => alphabet[value & 63]).join("");
      input.dispatchEvent(new Event("input", { bubbles: true }));
      input.focus();
      notify("Сгенерирован случайный пароль из 24 символов.");
    });
  });

  document.querySelectorAll("[data-copy-target]").forEach((button) => {
    button.addEventListener("click", async () => {
      const input = document.getElementById(button.dataset.copyTarget);
      if (!input) return;
      const value = input.value;
      if (!value) {
        notify("Нет данных для копирования.");
        return;
      }
      try {
        if (!navigator.clipboard || !window.isSecureContext) throw new Error("Clipboard unavailable");
        await navigator.clipboard.writeText(value);
        notify("Скопировано в буфер обмена.");
      } catch (_) {
        input.focus();
        input.select();
        let copied = false;
        try { copied = document.execCommand("copy"); } catch (_) { /* Keep text selected for manual copy. */ }
        notify(copied ? "Скопировано в буфер обмена." : "Текст выделен. Скопируйте его сочетанием клавиш или через меню браузера.");
      }
    });
  });

  document.querySelectorAll("[data-download-target]").forEach((button) => {
    button.addEventListener("click", () => {
      const input = document.getElementById(button.dataset.downloadTarget);
      if (!input || !input.value) {
        notify("Нет конфигурации для скачивания.");
        return;
      }
      const blob = new Blob([input.value], { type: "application/toml;charset=utf-8" });
      const url = URL.createObjectURL(blob);
      const link = document.createElement("a");
      link.href = url;
      link.download = button.dataset.downloadFilename || "trusttunnel.toml";
      document.body.appendChild(link);
      link.click();
      link.remove();
      window.setTimeout(() => URL.revokeObjectURL(url), 1000);
    });
  });

  document.querySelectorAll("[data-scroll-bottom]").forEach((button) => {
    button.addEventListener("click", () => {
      const target = document.getElementById(button.dataset.scrollBottom);
      if (!target) return;
      target.scrollTop = target.scrollHeight;
      target.focus({ preventScroll: true });
    });
  });
})();
