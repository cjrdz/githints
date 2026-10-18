#include "todomodel.h"

TodoModel::TodoModel(QObject *parent) : QAbstractListModel(parent) {}

int TodoModel::rowCount(const QModelIndex &) const { return items_.size(); }

QVariant TodoModel::data(const QModelIndex &index, int role) const
{
    if (role != Qt::DisplayRole) {
        return {};
    }
    return items_.at(index.row());
}

void TodoModel::add(const QString &text)
{
    beginInsertRows({}, items_.size(), items_.size());
    items_ << text;
    endInsertRows();
}
