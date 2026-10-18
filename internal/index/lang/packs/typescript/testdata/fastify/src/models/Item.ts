import { DataTypes, Model, Sequelize } from "sequelize";

const sequelize = new Sequelize("sqlite::memory:");

export class Item extends Model {}
Item.init({ name: DataTypes.STRING }, { sequelize });

export const Tag = sequelize.define("Tag", { label: DataTypes.STRING });
