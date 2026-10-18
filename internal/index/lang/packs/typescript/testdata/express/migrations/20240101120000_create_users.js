exports.up = function (knex) {
  return knex.schema.createTable("users", (t) => t.increments());
};

exports.down = function (knex) {
  return knex.schema.dropTable("users");
};
