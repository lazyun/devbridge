"use strict";

(function (root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  else root.DevBridgeDevelopment = api;
})(typeof window !== "undefined" ? window : globalThis, function () {
  function render(context, t, esc, options = {}) {
    if (options.loading) return `<span class="muted">${esc(t("development.loading"))}</span>`;
    if (options.error) return `<span class="development-error" role="alert">${esc(t("development.error"))}: ${esc(options.error)}</span>`;
    if (!context) return "";
    const repositories = context.repositories || [];
    if (!repositories.length) return `<span class="muted">${esc(t("development.noRepositories"))}</span>`;
    return `<div class="development-selector" data-testid="development-selector">${repositories.map((repo) => {
      const duplicateName = repositories.some((other) => other.repositoryId !== repo.repositoryId && other.repositoryName === repo.repositoryName);
      const current = repo.branchShortName || t("development.choose");
      const branches = repo.branches || [];
      const facts = [];
      if (repo.developmentRefStatus === "MISSING") facts.push(t("development.missing"));
      if (repo.refChanged === true) facts.push(t("development.moved"));
      if (repo.repositoryCheckoutMatchesDevelopmentRef === false) facts.push(t("development.checkoutMismatch"));
      const busy = options.pendingRepository === repo.repositoryId;
      return `<details class="development-repository"><summary>${esc(t("development.title"))}: <strong>${esc(current)}</strong> · ${esc(repo.repositoryName)}${duplicateName ? ` <span class="development-repository-id">(${esc(repo.repositoryId)})</span>` : ""}</summary><div class="development-options"><form data-development-form data-repository-id="${esc(repo.repositoryId)}"><label>${esc(t("development.repository", {name: repo.repositoryName}))}<select name="developmentRef" aria-label="${esc(t("development.title"))}" ${busy ? "disabled" : ""}><option value="">${esc(t("development.choose"))}</option>${branches.map((branch) => `<option value="${esc(branch.fullRef)}"${branch.fullRef === repo.developmentRef ? " selected" : ""}>${esc(branch.shortName)}${branch.repositoryCheckout ? ` · ${esc(t("development.checkout"))}` : ""}</option>`).join("")}</select></label><button class="button secondary" type="submit" ${busy || !branches.length ? "disabled" : ""}>${esc(t(busy ? "development.saving" : "development.select"))}</button></form>${!branches.length ? `<p>${esc(t("development.noBranches"))}</p>` : ""}${facts.length ? `<p class="development-facts">${esc(facts.join(" · "))}</p>` : ""}<p class="muted">${esc(t("development.note"))}</p></div></details>`;
    }).join("")}</div>`;
  }

  // Persist only an unacknowledged command identity, never selected branch truth.
  // A transport failure or page reload retries the exact same durable command.
  function command(storage, projectID, repositoryID, developmentRef, newID) {
    const key = `devbridge.console.developmentCommand:${JSON.stringify([projectID, repositoryID, developmentRef])}`;
    let requestID = storage.getItem(key);
    if (!requestID) { requestID = newID(); storage.setItem(key, requestID); }
    return {key, body: {project_id: projectID, repository_id: repositoryID, development_ref: developmentRef, request_id: requestID}};
  }
  return {render, command};
});
